package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

func (s *Server) workflowStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.workflowConfig(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "승인 정책을 불러올 수 없습니다")
		return
	}
	var pending int64
	_ = s.repo.Pool().QueryRow(r.Context(), `SELECT count(*) FROM approval_requests WHERE status='pending'`).Scan(&pending)
	writeData(w, http.StatusOK, map[string]any{
		"enabled": cfg.Enabled, "approvalEnabled": cfg.Enabled, "approvalPending": pending > 0,
		"pending": pending, "actions": cfg.Actions,
	})
}

func (s *Server) canReviewWorkflow(r *http.Request, cfg model.WorkflowConfig) bool {
	p := getPrincipal(r)
	if hasRole(p.User, model.RoleSuperAdmin) {
		return true
	}
	for _, role := range cfg.ApproverRoles {
		if slicesContains(p.User.Roles, role) {
			return true
		}
	}
	return false
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.workflowConfig(r)
	if err != nil || !s.canReviewWorkflow(r, cfg) {
		writeError(w, http.StatusForbidden, "not_approver", "현재 승인 정책의 검토 역할이 아닙니다")
		return
	}
	limit, offset := pagination(r)
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if status == "" {
		status = "pending"
	}
	if !slicesContains([]string{"pending", "approved", "rejected", "cancelled", "all"}, status) {
		writeError(w, http.StatusBadRequest, "invalid_status", "승인 상태가 올바르지 않습니다")
		return
	}
	rows, err := s.repo.Pool().Query(r.Context(), `SELECT a.id,a.action,a.target_type,a.target_id,a.requester_id,a.status,a.snapshot,COALESCE(a.reviewer_id,''),a.comment,a.requested_at,a.reviewed_at,u.username FROM approval_requests a JOIN users u ON u.id=a.requester_id WHERE ($1='all' OR a.status=$1) ORDER BY a.requested_at DESC LIMIT $2 OFFSET $3`, status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "승인 요청을 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var item model.Approval
		var encrypted []byte
		var requester string
		if err := rows.Scan(&item.ID, &item.Action, &item.TargetType, &item.TargetID, &item.RequesterID, &item.Status, &encrypted, &item.ReviewerID, &item.Comment, &item.RequestedAt, &item.ReviewedAt, &requester); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "승인 요청을 불러올 수 없습니다")
			return
		}
		plain, decryptErr := s.secrets.Decrypt(encrypted, "approval:"+item.ID+":snapshot")
		if decryptErr != nil {
			writeError(w, http.StatusInternalServerError, "snapshot_error", "승인 요청 스냅샷을 확인할 수 없습니다")
			return
		}
		var snapshot map[string]any
		_ = json.Unmarshal(plain, &snapshot)
		summary, _ := snapshot["contentPreview"].(string)
		items = append(items, map[string]any{
			"id": item.ID, "action": item.Action, "targetType": item.TargetType, "targetId": item.TargetID,
			"requesterId": item.RequesterID, "requesterUsername": requester, "status": item.Status,
			"snapshot": snapshot, "summary": summary, "reviewerId": item.ReviewerID, "comment": item.Comment,
			"requestedAt": item.RequestedAt, "createdAt": item.RequestedAt, "reviewedAt": item.ReviewedAt,
		})
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request) { s.reviewApproval(w, r, true) }
func (s *Server) reject(w http.ResponseWriter, r *http.Request)  { s.reviewApproval(w, r, false) }

func (s *Server) reviewApproval(w http.ResponseWriter, r *http.Request, approved bool) {
	cfg, err := s.workflowConfig(r)
	if err != nil || !s.canReviewWorkflow(r, cfg) {
		writeError(w, http.StatusForbidden, "not_approver", "현재 승인 정책의 검토 역할이 아닙니다")
		return
	}
	var input struct {
		Comment string `json:"comment"`
	}
	if r.ContentLength > 0 && !decodeJSON(w, r, &input) {
		return
	}
	input.Comment = strings.TrimSpace(input.Comment)
	if (!approved && input.Comment == "") || !utf8.ValidString(input.Comment) || utf8.RuneCountInString(input.Comment) > 2000 {
		writeError(w, http.StatusBadRequest, "comment_required", "반려 사유는 1~2,000자로 입력해 주세요")
		return
	}
	p := getPrincipal(r)
	tx, err := s.repo.Pool().Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "승인 요청을 처리할 수 없습니다")
		return
	}
	defer tx.Rollback(r.Context())
	var item model.Approval
	var encrypted []byte
	err = tx.QueryRow(r.Context(), `SELECT id,action,target_type,target_id,requester_id,status,snapshot,requested_at FROM approval_requests WHERE id=$1 FOR UPDATE`, chi.URLParam(r, "approvalID")).Scan(&item.ID, &item.Action, &item.TargetType, &item.TargetID, &item.RequesterID, &item.Status, &encrypted, &item.RequestedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "승인 요청을 찾을 수 없습니다")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "승인 요청을 처리할 수 없습니다")
		return
	}
	if item.Status != "pending" {
		writeError(w, http.StatusConflict, "already_reviewed", "이미 처리된 승인 요청입니다")
		return
	}
	if item.RequesterID == p.User.ID {
		writeError(w, http.StatusForbidden, "self_approval", "본인이 요청한 작업은 직접 승인할 수 없습니다")
		return
	}
	if _, err := s.secrets.Decrypt(encrypted, "approval:"+item.ID+":snapshot"); err != nil {
		writeError(w, http.StatusInternalServerError, "snapshot_error", "승인 스냅샷 무결성을 확인할 수 없습니다")
		return
	}
	status, postStatus, eventType := "rejected", "rejected", "approval_rejected"
	if approved {
		status, postStatus, eventType = "approved", "published", "approval_approved"
	}
	tag, err := tx.Exec(r.Context(), `UPDATE approval_requests SET status=$2,reviewer_id=$3,comment=$4,reviewed_at=now() WHERE id=$1 AND status='pending'`, item.ID, status, p.User.ID, input.Comment)
	if err != nil || tag.RowsAffected() != 1 {
		writeError(w, http.StatusConflict, "already_reviewed", "다른 검토자가 먼저 처리했습니다")
		return
	}
	if item.Action != "post.publish" || item.TargetType != "post" {
		writeError(w, http.StatusConflict, "unsupported_action", "현재 버전에서 실행할 수 없는 승인 작업입니다")
		return
	}
	postTag, err := tx.Exec(r.Context(), `UPDATE posts SET status=$2,published_at=CASE WHEN $2='published' THEN now() ELSE published_at END,updated_at=now() WHERE id=$1 AND status='pending_approval'`, item.TargetID, postStatus)
	if err != nil || postTag.RowsAffected() != 1 {
		writeError(w, http.StatusConflict, "target_changed", "승인 대상 Moin 상태가 변경되어 결정을 적용하지 않았습니다")
		return
	}
	if err := s.enqueueNotification(r.Context(), tx, item.RequesterID, p.User.ID, eventType, item.TargetID,
		map[string]string{"postId": item.TargetID, "body": input.Comment},
		fmt.Sprintf("notification:approval:%s:%s:%s", item.ID, status, item.RequesterID)); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "승인 결과를 적용할 수 없습니다")
		return
	}
	if approved {
		var authorID, content, kind, relatedAuthorID string
		err = tx.QueryRow(r.Context(), `SELECT p.author_id,p.content,p.kind,COALESCE(related.author_id,'')
			FROM posts p LEFT JOIN posts related ON related.id=COALESCE(p.reply_to_id,p.quote_post_id,p.remoin_post_id)
			WHERE p.id=$1`, item.TargetID).Scan(&authorID, &content, &kind, &relatedAuthorID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "승인 대상 Moin을 불러올 수 없습니다")
			return
		}
		if err := s.enqueueMentionNotifications(r.Context(), tx, authorID, item.TargetID, content); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "승인 알림을 저장할 수 없습니다")
			return
		}
		if relatedAuthorID != "" && relatedAuthorID != authorID {
			notificationType := "reply"
			if kind == "quote" {
				notificationType = "quote"
			} else if kind == "remoin" {
				notificationType = "remoin"
			}
			if err := s.enqueueNotification(r.Context(), tx, relatedAuthorID, authorID, notificationType, item.TargetID,
				map[string]string{"postId": item.TargetID}, fmt.Sprintf("notification:post:%s:%s:%s", item.TargetID, notificationType, relatedAuthorID)); err != nil {
				writeError(w, http.StatusInternalServerError, "storage_error", "승인 알림을 저장할 수 없습니다")
				return
			}
		}
	}
	if tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "승인 결과를 적용할 수 없습니다")
		return
	}
	s.audit(r, "approval."+status, "approval", item.ID, true, map[string]string{"targetId": item.TargetID, "comment": input.Comment})
	writeData(w, http.StatusOK, map[string]any{"id": item.ID, "status": status, "targetId": item.TargetID, "reviewedAt": time.Now().UTC()})
}
