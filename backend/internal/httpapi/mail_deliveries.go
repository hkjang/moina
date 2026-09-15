package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// mailDelivery is one message's record: when, which event, to whom, which
// subject, and whether it went out. Never the body — with it the log would be a
// second copy of everything that left the building.
type mailDelivery struct {
	ID           string    `json:"id"`
	Event        string    `json:"event"`
	UserID       string    `json:"userId,omitempty"`
	ActorID      string    `json:"actorId,omitempty"`
	Recipient    string    `json:"recipient"`
	Subject      string    `json:"subject"`
	Status       string    `json:"status"`
	Attempts     int       `json:"attempts"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

const (
	mailDeliveryQueued = "queued"
	mailDeliverySent   = "sent"
	mailDeliveryFailed = "failed"

	mailDeliverySubjectLimit = 300
	mailDeliveryErrorLimit   = 1000
)

// recordMailAttempt writes the row before the relay is contacted, so a crash
// mid-send still leaves a trace. A retry of the same message (the outbox
// re-runs the event with the same ID) bumps attempts instead of adding a row.
// Recording never fails the send: a lost log line is cheaper than a lost mail.
func (s *Server) recordMailAttempt(ctx context.Context, delivery mailDelivery) {
	if s.repo == nil {
		return
	}
	_, err := s.repo.Pool().Exec(ctx, `INSERT INTO mail_deliveries(id,event,user_id,actor_id,recipient,subject,status,attempts,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,'queued',1,now(),now())
		ON CONFLICT(id) DO UPDATE SET status='queued',attempts=mail_deliveries.attempts+1,updated_at=now()`,
		delivery.ID, delivery.Event, delivery.UserID, delivery.ActorID, delivery.Recipient, truncateRunes(delivery.Subject, mailDeliverySubjectLimit))
	if err != nil {
		slog.WarnContext(ctx, "메일 발송 기록 실패", "delivery_id", delivery.ID, "event", delivery.Event, "error", err)
	}
}

func (s *Server) completeMailAttempt(ctx context.Context, delivery mailDelivery, cause error) {
	status, message := mailDeliverySent, ""
	if cause != nil {
		status, message = mailDeliveryFailed, truncateRunes(cause.Error(), mailDeliveryErrorLimit)
		slog.WarnContext(ctx, "메일 발송 실패", "delivery_id", delivery.ID, "event", delivery.Event, "recipient", delivery.Recipient, "error", cause)
	}
	if s.repo == nil {
		return
	}
	if _, err := s.repo.Pool().Exec(ctx, `UPDATE mail_deliveries SET status=$2,error_message=$3,updated_at=now() WHERE id=$1`, delivery.ID, status, message); err != nil {
		slog.WarnContext(ctx, "메일 발송 결과 기록 실패", "delivery_id", delivery.ID, "error", err)
	}
}

// adminListMailDeliveries answers "did it go out?" — newest first, with a count
// per status so a failing relay shows up before anyone scrolls.
func (s *Server) adminListMailDeliveries(w http.ResponseWriter, r *http.Request) {
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && status != mailDeliveryQueued && status != mailDeliverySent && status != mailDeliveryFailed {
		writeError(w, http.StatusBadRequest, "invalid_status", "발송 상태는 queued, sent, failed 중 하나여야 합니다")
		return
	}
	limit, _, ok := pagination(w, r)
	if !ok {
		return
	}
	rows, err := s.repo.Pool().Query(r.Context(), `SELECT id,event,user_id,actor_id,recipient,subject,status,attempts,error_message,created_at,updated_at
		FROM mail_deliveries WHERE ($1='' OR status=$1) ORDER BY created_at DESC, id LIMIT $2`, status, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "메일 발송 기록을 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]mailDelivery, 0, limit)
	for rows.Next() {
		var item mailDelivery
		if err := rows.Scan(&item.ID, &item.Event, &item.UserID, &item.ActorID, &item.Recipient, &item.Subject, &item.Status, &item.Attempts, &item.ErrorMessage, &item.CreatedAt, &item.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "메일 발송 기록을 불러올 수 없습니다")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "메일 발송 기록을 불러올 수 없습니다")
		return
	}
	counts, err := s.repo.Pool().Query(r.Context(), `SELECT status,count(*) FROM mail_deliveries GROUP BY status`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "메일 발송 기록을 불러올 수 없습니다")
		return
	}
	defer counts.Close()
	summary := map[string]int64{mailDeliveryQueued: 0, mailDeliverySent: 0, mailDeliveryFailed: 0}
	var total int64
	for counts.Next() {
		var key string
		var count int64
		if err := counts.Scan(&key, &count); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "메일 발송 기록을 불러올 수 없습니다")
			return
		}
		summary[key] = count
		total += count
	}
	if err := counts.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "메일 발송 기록을 불러올 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"items": items, "status": status, "limit": limit,
		"summary": map[string]any{"total": total, "status": summary},
	})
}
