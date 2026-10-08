// Package scoring tiêu thụ sự kiện kyc.application.submitted và suy ra điểm
// tín dụng. Nó là phía đọc của cặp bất đồng bộ: không có đường ghi qua HTTP,
// một consumer Kafka, một endpoint truy vấn.
//
// Hàm tính điểm là thứ vô nghĩa nhưng tất định — một phép băm chứ không phải mô
// hình. Điều đó có chủ ý: một bài lab cần kết quả đoán trước và khẳng định
// được, còn những kiểu hỏng đáng quan tâm ở đây là hạ tầng (consumer lag,
// rebalance giữa lúc rolling update, gửi lại theo at-least-once) chứ không phải
// thống kê.
package scoring

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kalapa-lab/kalapa-platform/internal/platform/db"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/httpx"
)

var Schema = []string{
	`CREATE TABLE IF NOT EXISTS credit_scores (
		application_id UUID PRIMARY KEY,
		national_id    TEXT        NOT NULL,
		score          INT         NOT NULL,
		band           TEXT        NOT NULL,
		decision       TEXT        NOT NULL,
		scored_at      TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS credit_scores_scored_at_idx
		ON credit_scores (scored_at DESC)`,
}

type Score struct {
	ApplicationID string    `json:"application_id"`
	NationalID    string    `json:"national_id"`
	Score         int       `json:"score"`
	Band          string    `json:"band"`
	Decision      string    `json:"decision"`
	ScoredAt      time.Time `json:"scored_at"`
}

// applicationSubmitted phản chiếu kyc.ApplicationSubmitted. Nó được chép lại
// thay vì import một cách có chủ ý: consumer phải tiến hoá được độc lập với
// kiểu dữ liệu nội bộ của producer, và chép đúng vài trường mình thực sự đọc là
// cách giữ cho điều đó luôn đúng.
type applicationSubmitted struct {
	ApplicationID string    `json:"application_id"`
	NationalID    string    `json:"national_id"`
	FullName      string    `json:"full_name"`
	DateOfBirth   string    `json:"date_of_birth"`
	SubmittedAt   time.Time `json:"submitted_at"`
}

type Service struct {
	DB     *db.DB
	Tracer trace.Tracer
	Log    *slog.Logger
}

func (s *Service) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /scoring/scores/{applicationId}", s.get)
	mux.HandleFunc("GET /scoring/scores", s.list)
	return mux
}

// Handle là events.Handler được truyền vào vòng lặp consumer.
func (s *Service) Handle(ctx context.Context, key string, value []byte) error {
	ctx, span := s.Tracer.Start(ctx, "scoring.handle")
	defer span.End()

	var evt applicationSubmitted
	if err := json.Unmarshal(value, &evt); err != nil {
		// Một bản ghi hỏng định dạng là lỗi vĩnh viễn: thử lại mãi sẽ làm nghẽn
		// partition. Ghi nhận lại rồi để offset tiến lên.
		span.RecordError(err)
		return err
	}
	if evt.ApplicationID == "" {
		evt.ApplicationID = key
	}

	score := compute(evt.NationalID)
	band, decision := classify(score)
	span.SetAttributes(
		attribute.String("kyc.application_id", evt.ApplicationID),
		attribute.Int("scoring.score", score),
		attribute.String("scoring.decision", decision),
	)

	// ON CONFLICT làm handler trở nên idempotent — đó là cái giá của việc giao
	// nhận at-least-once: cùng một sự kiện sẽ được gửi lại mỗi khi pod chết
	// giữa lúc xử lý xong và lúc commit.
	_, err := s.DB.Exec(ctx,
		`INSERT INTO credit_scores (application_id, national_id, score, band, decision, scored_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (application_id) DO UPDATE
		   SET score = EXCLUDED.score,
		       band = EXCLUDED.band,
		       decision = EXCLUDED.decision,
		       scored_at = now()`,
		evt.ApplicationID, evt.NationalID, score, band, decision)
	if err != nil {
		span.RecordError(err)
		return err
	}

	s.Log.InfoContext(ctx, "application scored",
		slog.String("application_id", evt.ApplicationID),
		slog.Int("score", score),
		slog.String("decision", decision))
	return nil
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.Tracer.Start(r.Context(), "scoring.get")
	defer span.End()

	id := r.PathValue("applicationId")
	if _, err := uuid.Parse(id); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "applicationId must be a UUID")
		return
	}

	var sc Score
	err := s.DB.QueryRow(ctx,
		`SELECT application_id, national_id, score, band, decision, scored_at
		 FROM credit_scores WHERE application_id = $1`, id,
	).Scan(&sc.ApplicationID, &sc.NationalID, &sc.Score, &sc.Band, &sc.Decision, &sc.ScoredAt)

	if errors.Is(err, pgx.ErrNoRows) {
		// 404 ở đây thường nghĩa là "CHƯA chấm điểm" chứ không phải "sẽ không
		// bao giờ có" — khoảng trễ bất đồng bộ được phơi ra cho người gọi thấy,
		// và như vậy là trung thực.
		httpx.WriteError(w, http.StatusNotFound, "no score for this application yet")
		return
	}
	if err != nil {
		s.Log.ErrorContext(ctx, "query failed", slog.Any("error", err))
		httpx.WriteError(w, http.StatusInternalServerError, "query failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sc)
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.Tracer.Start(r.Context(), "scoring.list")
	defer span.End()

	rows, err := s.DB.Query(ctx,
		`SELECT application_id, national_id, score, band, decision, scored_at
		 FROM credit_scores ORDER BY scored_at DESC LIMIT 50`)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	items := make([]Score, 0, 50)
	for rows.Next() {
		var sc Score
		if err := rows.Scan(&sc.ApplicationID, &sc.NationalID, &sc.Score, &sc.Band, &sc.Decision, &sc.ScoredAt); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		items = append(items, sc)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// compute ánh xạ số CCCD vào khoảng 300-850, tức dải điểm FICO thông dụng.
func compute(nationalID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(nationalID))
	return 300 + int(h.Sum32()%551)
}

func classify(score int) (band, decision string) {
	switch {
	case score >= 750:
		return "EXCELLENT", "APPROVED"
	case score >= 670:
		return "GOOD", "APPROVED"
	case score >= 580:
		return "FAIR", "MANUAL_REVIEW"
	default:
		return "POOR", "REJECTED"
	}
}
