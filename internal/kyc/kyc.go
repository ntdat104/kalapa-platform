// Package kyc là service tiếp nhận: nó nhận một hồ sơ xác minh danh tính, lưu
// lại, rồi thông báo lên Kafka để bước chấm điểm phía sau xử lý.
//
// Phần nghiệp vụ cố tình làm mỏng. Thứ nó tồn tại để minh hoạ là hình dạng của
// một đường ghi phải đúng đắn khi chạy trên Kubernetes: một lần ghi database và
// một lần phát sự kiện không được phép mâu thuẫn nhau, một readiness check phản
// ánh cả hai phụ thuộc, và một trace sống sót qua chặng bất đồng bộ.
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

// Schema được áp dụng lúc khởi động. Mọi câu lệnh đều IF NOT EXISTS vì
// migration chạy ở mỗi lần pod khởi động — xem db.Migrate.
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

// ApplicationSubmitted là hợp đồng sự kiện với service scoring. Nó được đặt
// thành một kiểu có tên một cách có chủ ý: topic Kafka chính là một API, và coi
// nó như API mới là điều cho phép hai service được deploy độc lập nhau.
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

// Routes trả về mux cho port nghiệp vụ. Pattern dùng cú pháp method + ký tự
// đại diện của Go 1.22, để middleware metric đọc được label route hữu hạn.
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

	// Phát sự kiện SAU khi commit. Thứ tự ngược lại sẽ để scoring nhận một sự
	// kiện cho bản ghi chưa bao giờ tồn tại. Kiểu hỏng còn lại — bản ghi đã
	// lưu, sự kiện bị mất — chính là thứ mà hệ thống thật giải bằng mẫu
	// transactional outbox; xem lab 6 ở docs/07-labs.md, bài đó bắt bạn tự xây.
	evt := ApplicationSubmitted{
		ApplicationID: app.ID,
		NationalID:    app.NationalID,
		FullName:      app.FullName,
		DateOfBirth:   app.DateOfBirth,
		SubmittedAt:   app.SubmittedAt,
	}
	if err := s.Producer.Publish(ctx, app.ID, evt); err != nil {
		// Trả 202 chứ không phải 500: hồ sơ đã được lưu bền, chỉ việc thông báo
		// xuống dưới là hỏng. Trả 500 sẽ mời người gọi thử lại và tạo ra một hồ
		// sơ trùng.
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
