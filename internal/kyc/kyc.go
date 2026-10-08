// Package kyc is the intake service: it accepts an identity-verification
// application, persists it, and announces it on Kafka for downstream scoring.
//
// The business logic is deliberately thin. What it exists to demonstrate is
// the shape of a write path that has to be correct under Kubernetes: a
// database write and an event publish that must not disagree, a readiness
// check that reflects both dependencies, and a trace that survives the hop.
package kyc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kalapa-lab/kalapa-platform/internal/platform/db"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/events"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/httpx"
)

// Schema is applied at startup. Every statement is IF NOT EXISTS because the
// migration runs on every pod start — see db.Migrate.
var Schema = []string{
	`CREATE TABLE IF NOT EXISTS kyc_applications (
		id            UUID PRIMARY KEY,
		national_id   TEXT        NOT NULL,
		full_name     TEXT        NOT NULL,
		date_of_birth DATE,
		status        TEXT        NOT NULL DEFAULT 'PENDING',
		submitted_at  TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS kyc_applications_national_id_idx
		ON kyc_applications (national_id)`,
	`CREATE INDEX IF NOT EXISTS kyc_applications_submitted_at_idx
		ON kyc_applications (submitted_at DESC)`,
}

type Application struct {
	ID          string    `json:"id"`
	NationalID  string    `json:"national_id"`
	FullName    string    `json:"full_name"`
	DateOfBirth string    `json:"date_of_birth,omitempty"`
	Status      string    `json:"status"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type submitRequest struct {
	NationalID  string `json:"national_id"`
	FullName    string `json:"full_name"`
	DateOfBirth string `json:"date_of_birth"`
}

// ApplicationSubmitted is the event contract with the scoring service. It is a
// named type on purpose: the Kafka topic is an API, and treating it as one is
// what lets the two services be deployed independently.
type ApplicationSubmitted struct {
	ApplicationID string    `json:"application_id"`
	NationalID    string    `json:"national_id"`
	FullName      string    `json:"full_name"`
	DateOfBirth   string    `json:"date_of_birth,omitempty"`
	SubmittedAt   time.Time `json:"submitted_at"`
}

type Service struct {
	DB       *db.DB
	Producer *events.Producer
	Tracer   trace.Tracer
	Log      *slog.Logger
}

// Routes returns the mux for the business port. Patterns use Go 1.22 method +
// wildcard syntax so the metrics middleware can read a bounded route label.
func (s *Service) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /kyc/applications", s.submit)
	mux.HandleFunc("GET /kyc/applications/{id}", s.get)
	mux.HandleFunc("GET /kyc/applications", s.list)
	return mux
}

func (s *Service) submit(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.Tracer.Start(r.Context(), "kyc.submit")
	defer span.End()

	var req submitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := req.validate(); err != nil {
		httpx.WriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	app := Application{
		ID:          uuid.NewString(),
		NationalID:  req.NationalID,
		FullName:    strings.TrimSpace(req.FullName),
		DateOfBirth: req.DateOfBirth,
		Status:      "PENDING",
		SubmittedAt: time.Now().UTC(),
	}
	span.SetAttributes(attribute.String("kyc.application_id", app.ID))

	if err := s.insert(ctx, app); err != nil {
		s.Log.ErrorContext(ctx, "insert application failed", slog.Any("error", err))
		httpx.WriteError(w, http.StatusInternalServerError, "could not store application")
		return
	}

	// Publish after the commit. The opposite order would let scoring receive
	// an event for a row that never materialised. The remaining failure mode —
	// row written, event lost — is the one a real system solves with the
	// transactional outbox pattern; see docs/07-labs.md lab 6, which has you
	// build it.
	evt := ApplicationSubmitted{
		ApplicationID: app.ID,
		NationalID:    app.NationalID,
		FullName:      app.FullName,
		DateOfBirth:   app.DateOfBirth,
		SubmittedAt:   app.SubmittedAt,
	}
	if err := s.Producer.Publish(ctx, app.ID, evt); err != nil {
		// 202 rather than 500: the application is durably stored, only the
		// downstream notification failed. Returning 500 would invite the
		// client to retry and create a duplicate application.
		s.Log.ErrorContext(ctx, "publish failed, application stored", slog.Any("error", err))
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
			"application": app,
			"warning":     "stored, but scoring was not notified",
		})
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, app)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.Tracer.Start(r.Context(), "kyc.get")
	defer span.End()

	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "id must be a UUID")
		return
	}

	var app Application
	var dob *time.Time
	err := s.DB.QueryRow(ctx,
		`SELECT id, national_id, full_name, date_of_birth, status, submitted_at
		 FROM kyc_applications WHERE id = $1`, id,
	).Scan(&app.ID, &app.NationalID, &app.FullName, &dob, &app.Status, &app.SubmittedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "application not found")
		return
	}
	if err != nil {
		s.Log.ErrorContext(ctx, "query failed", slog.Any("error", err))
		httpx.WriteError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if dob != nil {
		app.DateOfBirth = dob.Format("2006-01-02")
	}
	httpx.WriteJSON(w, http.StatusOK, app)
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.Tracer.Start(r.Context(), "kyc.list")
	defer span.End()

	rows, err := s.DB.Query(ctx,
		`SELECT id, national_id, full_name, status, submitted_at
		 FROM kyc_applications ORDER BY submitted_at DESC LIMIT 50`)
	if err != nil {
		s.Log.ErrorContext(ctx, "list failed", slog.Any("error", err))
		httpx.WriteError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	items := make([]Application, 0, 50)
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.NationalID, &a.FullName, &a.Status, &a.SubmittedAt); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		items = append(items, a)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Service) insert(ctx context.Context, app Application) error {
	ctx, span := s.Tracer.Start(ctx, "kyc.insert")
	defer span.End()

	var dob any
	if app.DateOfBirth != "" {
		dob = app.DateOfBirth
	}
	_, err := s.DB.Exec(ctx,
		`INSERT INTO kyc_applications (id, national_id, full_name, date_of_birth, status, submitted_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		app.ID, app.NationalID, app.FullName, dob, app.Status, app.SubmittedAt)
	return err
}

func (r submitRequest) validate() error {
	if len(strings.TrimSpace(r.FullName)) < 2 {
		return fmt.Errorf("full_name is required")
	}
	id := strings.TrimSpace(r.NationalID)
	if len(id) < 9 || len(id) > 12 {
		return fmt.Errorf("national_id must be 9-12 characters")
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return fmt.Errorf("national_id must be digits only")
		}
	}
	if r.DateOfBirth != "" {
		if _, err := time.Parse("2006-01-02", r.DateOfBirth); err != nil {
			return fmt.Errorf("date_of_birth must be YYYY-MM-DD")
		}
	}
	return nil
}
