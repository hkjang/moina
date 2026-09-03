package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

var roleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,49}$`)

var permissionCatalog = []string{
	"admin:access", "users:manage", "posts:manage", "moderation:manage", "approvals:review",
	"roles:manage", "settings:manage", "audit:read", "keys:manage", "posts:read", "posts:write",
	"social:write", "ai:use", "mcp:use", "outbox:manage",
}

func (s *Server) adminStats(w http.ResponseWriter, r *http.Request) {
	var users, todayPosts, pendingReports, pendingApprovals int64
	err := s.repo.Pool().QueryRow(r.Context(), `SELECT
		(SELECT count(*) FROM users),
		(SELECT count(*) FROM posts WHERE created_at>=date_trunc('day',now())),
		(SELECT count(*) FROM reports WHERE status IN ('open','reviewing')),
		(SELECT count(*) FROM approval_requests WHERE status='pending')`).Scan(&users, &todayPosts, &pendingReports, &pendingApprovals)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "관리 통계를 불러올 수 없습니다")
		return
	}
	ai, _ := s.aiConfig(r)
	writeData(w, http.StatusOK, map[string]any{
		"userCount": users, "todayPostCount": todayPosts, "pendingReportCount": pendingReports,
		"pendingApprovalCount": pendingApprovals, "databaseStatus": "active", "websocketStatus": "active", "aiEnabled": ai.Enabled,
	})
}

func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := pagination(w, r)
	if !ok {
		return
	}
	rows, err := s.repo.Pool().Query(r.Context(), `SELECT `+userSelectColumns+` FROM users ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "사용자 목록을 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]model.User, 0)
	for rows.Next() {
		user, scanErr := scanUserRow(rows)
		if scanErr != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "사용자 목록을 불러올 수 없습니다")
			return
		}
		items = append(items, user)
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

type adminUserInput struct {
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	Email       string   `json:"email"`
	Password    string   `json:"password"`
	Roles       []string `json:"roles"`
	Active      *bool    `json:"active"`
}

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	var input adminUserInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Username, input.DisplayName, input.Email = strings.TrimSpace(input.Username), strings.TrimSpace(input.DisplayName), strings.TrimSpace(input.Email)
	if len(input.Roles) == 0 {
		input.Roles = []string{model.RoleMember}
	}
	if !validUsername(input.Username) || !validDisplayName(input.DisplayName) || !validEmail(input.Email) || !validPassword(input.Password) || !s.rolesExist(r, input.Roles) {
		writeError(w, http.StatusBadRequest, "invalid_user", "사용자 정보, 초기 비밀번호 또는 역할이 올바르지 않습니다")
		return
	}
	if !s.canAssignRoles(r, input.Roles) {
		writeError(w, http.StatusForbidden, "role_escalation_forbidden", "현재 관리자 권한을 넘는 역할은 부여할 수 없습니다")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password_error", "비밀번호를 처리할 수 없습니다")
		return
	}
	user, err := scanUserRow(s.repo.Pool().QueryRow(r.Context(), `INSERT INTO users(id,username,display_name,email,password_hash,account_type,provider,roles,active) VALUES($1,$2,$3,$4,$5,'human','local',$6,true) RETURNING `+userSelectColumns, secure.NewID("usr"), input.Username, input.DisplayName, input.Email, string(hash), uniqueStrings(input.Roles)))
	if store.IsConflict(err) {
		writeError(w, http.StatusConflict, "username_taken", "이미 사용 중인 사용자 이름입니다")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "사용자를 추가할 수 없습니다")
		return
	}
	s.audit(r, "admin.user.create", "user", user.ID, true, map[string]any{"roles": user.Roles})
	writeData(w, http.StatusCreated, user)
}

func (s *Server) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DisplayName *string  `json:"displayName"`
		Email       *string  `json:"email"`
		Roles       []string `json:"roles"`
		Active      *bool    `json:"active"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	targetID := chi.URLParam(r, "userID")
	target, err := s.repo.UserByID(r.Context(), targetID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "사용자를 찾을 수 없습니다")
		return
	}
	if slices.Contains(target.Roles, model.RoleSuperAdmin) && !hasRole(getPrincipal(r).User, model.RoleSuperAdmin) {
		writeError(w, http.StatusForbidden, "super_admin_protected", "최고 관리자 계정은 최고 관리자만 변경할 수 있습니다")
		return
	}
	if input.DisplayName != nil {
		value := strings.TrimSpace(*input.DisplayName)
		if !validDisplayName(value) {
			writeError(w, http.StatusBadRequest, "invalid_name", "표시 이름이 올바르지 않습니다")
			return
		}
		input.DisplayName = &value
	}
	if input.Email != nil {
		value := strings.TrimSpace(*input.Email)
		if !validEmail(value) {
			writeError(w, http.StatusBadRequest, "invalid_email", "이메일이 올바르지 않습니다")
			return
		}
		input.Email = &value
	}
	if input.Roles != nil {
		input.Roles = uniqueStrings(input.Roles)
		if len(input.Roles) == 0 || !s.rolesExist(r, input.Roles) || !s.canAssignRoles(r, input.Roles) {
			writeError(w, http.StatusForbidden, "invalid_roles", "역할이 없거나 현재 관리자 권한을 넘습니다")
			return
		}
	}
	if targetID == getPrincipal(r).User.ID && input.Active != nil && !*input.Active {
		writeError(w, http.StatusConflict, "self_deactivation", "현재 로그인한 계정은 비활성화할 수 없습니다")
		return
	}
	removesSuper := slices.Contains(target.Roles, model.RoleSuperAdmin) && input.Roles != nil && !slices.Contains(input.Roles, model.RoleSuperAdmin)
	deactivatesSuper := slices.Contains(target.Roles, model.RoleSuperAdmin) && input.Active != nil && !*input.Active
	if removesSuper || deactivatesSuper {
		var count int
		_ = s.repo.Pool().QueryRow(r.Context(), `SELECT count(*) FROM users WHERE active AND roles @> ARRAY['super_admin']::text[]`).Scan(&count)
		if count <= 1 {
			writeError(w, http.StatusConflict, "last_super_admin", "마지막 최고 관리자는 변경할 수 없습니다")
			return
		}
	}
	user, err := scanUserRow(s.repo.Pool().QueryRow(r.Context(), `UPDATE users SET display_name=COALESCE($2,display_name),email=COALESCE($3,email),roles=COALESCE($4,roles),active=COALESCE($5,active),updated_at=now() WHERE id=$1 RETURNING `+userSelectColumns, targetID, input.DisplayName, input.Email, nullableStrings(input.Roles), input.Active))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "사용자를 변경할 수 없습니다")
		return
	}
	if input.Active != nil && !*input.Active {
		_ = s.repo.DeleteUserSessions(r.Context(), targetID)
	}
	s.audit(r, "admin.user.update", "user", targetID, true, map[string]any{"roles": input.Roles, "active": input.Active})
	writeData(w, http.StatusOK, user)
}

func nullableStrings(values []string) any {
	if values == nil {
		return nil
	}
	return values
}

func (s *Server) adminResetPassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !validPassword(input.Password) {
		writeError(w, http.StatusBadRequest, "invalid_password", "비밀번호는 12자 이상 72바이트 이하여야 합니다")
		return
	}
	target, lookupErr := s.repo.UserByID(r.Context(), chi.URLParam(r, "userID"))
	if lookupErr != nil {
		writeError(w, http.StatusNotFound, "not_found", "사용자를 찾을 수 없습니다")
		return
	}
	if slices.Contains(target.Roles, model.RoleSuperAdmin) && !hasRole(getPrincipal(r).User, model.RoleSuperAdmin) {
		writeError(w, http.StatusForbidden, "super_admin_protected", "최고 관리자 비밀번호는 최고 관리자만 초기화할 수 있습니다")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password_error", "비밀번호를 처리할 수 없습니다")
		return
	}
	id := chi.URLParam(r, "userID")
	if err := s.repo.UpdatePassword(r.Context(), id, string(hash)); store.IsNotFound(err) {
		writeError(w, http.StatusConflict, "not_local_user", "로컬 사용자만 비밀번호를 초기화할 수 있습니다")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "비밀번호를 초기화할 수 없습니다")
		return
	}
	_ = s.repo.DeleteUserSessions(r.Context(), id)
	s.audit(r, "admin.user.password.reset", "user", id, true, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminListPosts(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := pagination(w, r)
	if !ok {
		return
	}
	rows, err := s.repo.Pool().Query(r.Context(), `SELECT p.id,p.content,p.kind,p.visibility,p.status,p.author_id,u.username,p.created_at,p.updated_at,(SELECT count(*) FROM reports WHERE target_type='post' AND target_id=p.id) FROM posts p JOIN users u ON u.id=p.author_id ORDER BY p.created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "콘텐츠 목록을 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, content, kind, visibility, status, authorID, authorUsername string
		var createdAt, updatedAt time.Time
		var reportCount int64
		if rows.Scan(&id, &content, &kind, &visibility, &status, &authorID, &authorUsername, &createdAt, &updatedAt, &reportCount) != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "콘텐츠 목록을 불러올 수 없습니다")
			return
		}
		items = append(items, map[string]any{"id": id, "content": content, "kind": kind, "visibility": visibility, "status": status, "authorId": authorID, "authorUsername": authorUsername, "reportCount": reportCount, "createdAt": createdAt, "updatedAt": updatedAt})
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (s *Server) adminUpdatePost(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	if input.Status != "deleted" {
		writeError(w, http.StatusBadRequest, "invalid_status", "관리 콘텐츠 API는 soft-delete만 지원합니다. 승인 상태는 승인 API로 변경해 주세요")
		return
	}
	id := chi.URLParam(r, "postID")
	affected, err := s.softDeletePost(r.Context(), id, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "콘텐츠를 삭제할 수 없습니다")
		return
	}
	if affected == 0 {
		writeError(w, http.StatusNotFound, "not_found", "콘텐츠를 찾을 수 없습니다")
		return
	}
	s.audit(r, "admin.post.status", "post", id, true, map[string]string{"status": input.Status})
	writeData(w, http.StatusOK, map[string]string{"id": id, "status": input.Status})
}

func (s *Server) adminDeletePost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.NoBody
	r.ContentLength = 0
	r.Header.Set("Content-Type", "application/json")
	// Keep the legacy DELETE alias recoverable as the same soft-delete operation.
	id := chi.URLParam(r, "postID")
	affected, err := s.softDeletePost(r.Context(), id, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "콘텐츠를 삭제할 수 없습니다")
		return
	}
	if affected == 0 {
		writeError(w, http.StatusNotFound, "not_found", "콘텐츠를 찾을 수 없습니다")
		return
	}
	s.audit(r, "admin.post.status", "post", id, true, map[string]string{"status": "deleted"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) softDeletePost(ctx context.Context, id string, onlyIfActive bool) (int64, error) {
	tx, err := s.repo.Pool().Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	query := `UPDATE posts SET status='deleted',content='',deleted_at=now(),updated_at=now() WHERE id=$1`
	if onlyIfActive {
		query += ` AND status<>'deleted'`
	}
	tag, err := tx.Exec(ctx, query, id)
	if err != nil || tag.RowsAffected() == 0 {
		return tag.RowsAffected(), err
	}
	if _, err := tx.Exec(ctx, `UPDATE approval_requests SET status='cancelled',reviewed_at=now(),comment='관리자 콘텐츠 삭제로 자동 취소' WHERE target_type='post' AND target_id=$1 AND status='pending'`, id); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Server) adminListReports(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := pagination(w, r)
	if !ok {
		return
	}
	rows, err := s.repo.Pool().Query(r.Context(), `SELECT r.id,r.reporter_id,u.username,r.target_type,r.target_id,r.reason,r.detail,r.status,r.resolution,COALESCE(r.moderator_id,''),r.created_at,r.resolved_at FROM reports r JOIN users u ON u.id=r.reporter_id ORDER BY CASE WHEN r.status IN ('open','reviewing') THEN 0 ELSE 1 END,r.created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "신고 목록을 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var report model.Report
		var username string
		if rows.Scan(&report.ID, &report.ReporterID, &username, &report.TargetType, &report.TargetID, &report.Reason, &report.Detail, &report.Status, &report.Resolution, &report.ModeratorID, &report.CreatedAt, &report.ResolvedAt) != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "신고 목록을 불러올 수 없습니다")
			return
		}
		items = append(items, map[string]any{"id": report.ID, "reporterId": report.ReporterID, "reporterUsername": username, "targetType": report.TargetType, "targetId": report.TargetID, "target": report.TargetType + ":" + report.TargetID, "reason": report.Reason, "detail": report.Detail, "status": report.Status, "resolution": report.Resolution, "moderatorId": report.ModeratorID, "createdAt": report.CreatedAt, "resolvedAt": report.ResolvedAt})
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (s *Server) adminResolveReport(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	s.resolveReport(w, r, input.Status, input.Resolution)
}

func (s *Server) adminResolveReportAlias(w http.ResponseWriter, r *http.Request) {
	status := "resolved"
	if strings.HasSuffix(r.URL.Path, "/reject") {
		status = "dismissed"
	}
	var input struct {
		Resolution string `json:"resolution"`
	}
	if r.ContentLength > 0 && !decodeJSON(w, r, &input) {
		return
	}
	s.resolveReport(w, r, status, input.Resolution)
}

func (s *Server) resolveReport(w http.ResponseWriter, r *http.Request, status, resolution string) {
	status, resolution = strings.ToLower(strings.TrimSpace(status)), strings.TrimSpace(resolution)
	if !slicesContains([]string{"reviewing", "resolved", "dismissed"}, status) || !utf8.ValidString(resolution) || utf8.RuneCountInString(resolution) > 2000 || (status != "reviewing" && resolution == "") {
		writeError(w, http.StatusBadRequest, "invalid_resolution", "신고 상태 또는 검토 메모가 올바르지 않습니다")
		return
	}
	id := chi.URLParam(r, "reportID")
	resolved := status == "resolved" || status == "dismissed"
	tag, err := s.repo.Pool().Exec(r.Context(), `UPDATE reports SET status=$2,resolution=$3,moderator_id=$4,resolved_at=CASE WHEN $5 THEN now() ELSE NULL END WHERE id=$1`, id, status, resolution, getPrincipal(r).User.ID, resolved)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "신고를 찾을 수 없습니다")
		return
	}
	if resolved {
		_, _ = s.repo.Pool().Exec(r.Context(), `INSERT INTO moderation_actions(id,report_id,moderator_id,action,target_type,target_id,reason) SELECT $1,id,$2,$3,target_type,target_id,$4 FROM reports WHERE id=$5`, secure.NewID("mod"), getPrincipal(r).User.ID, status, resolution, id)
	}
	s.audit(r, "admin.report."+status, "report", id, true, map[string]string{"resolution": resolution})
	writeData(w, http.StatusOK, map[string]string{"id": id, "status": status, "resolution": resolution})
}

func (s *Server) adminListRoles(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.Pool().Query(r.Context(), `SELECT r.name,r.description,r.system,COALESCE(array_agg(rp.permission ORDER BY rp.permission) FILTER (WHERE rp.permission IS NOT NULL),ARRAY[]::text[]) FROM roles r LEFT JOIN role_permissions rp ON rp.role_name=r.name GROUP BY r.name ORDER BY r.system DESC,r.name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "역할 정책을 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var name, description string
		var system bool
		var permissions []string
		if rows.Scan(&name, &description, &system, &permissions) != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "역할 정책을 불러올 수 없습니다")
			return
		}
		items = append(items, map[string]any{"name": name, "description": description, "system": system, "permissions": permissions})
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "permissions": permissionCatalog})
}

func (s *Server) adminPutRoles(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Roles []struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Permissions []string `json:"permissions"`
		} `json:"roles"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Roles) == 0 || len(input.Roles) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_roles", "역할 정책이 비어 있거나 너무 많습니다")
		return
	}
	seen := map[string]bool{}
	for index := range input.Roles {
		role := &input.Roles[index]
		role.Name, role.Description = strings.ToLower(strings.TrimSpace(role.Name)), strings.TrimSpace(role.Description)
		role.Permissions = uniqueStrings(role.Permissions)
		if !roleNamePattern.MatchString(role.Name) || seen[role.Name] || len(role.Permissions) == 0 || utf8.RuneCountInString(role.Description) > 500 {
			writeError(w, http.StatusBadRequest, "invalid_roles", "역할 이름, 설명 또는 권한이 올바르지 않습니다")
			return
		}
		seen[role.Name] = true
		if role.Name == model.RoleSuperAdmin && !slices.Equal(role.Permissions, []string{"*"}) {
			writeError(w, http.StatusConflict, "super_admin_protected", "super_admin의 전체 권한은 변경할 수 없습니다")
			return
		}
		for _, permission := range role.Permissions {
			if permission != "*" && (!permissionPattern.MatchString(permission) || !slicesContains(permissionCatalog, permission)) {
				writeError(w, http.StatusBadRequest, "invalid_permission", "지원하지 않는 권한이 포함되어 있습니다")
				return
			}
			if role.Name != model.RoleSuperAdmin && !hasRole(getPrincipal(r).User, model.RoleSuperAdmin) && !hasPermission(getPrincipal(r).Permissions, permission) {
				writeError(w, http.StatusForbidden, "permission_escalation", "현재 관리자 권한을 넘는 역할 권한은 부여할 수 없습니다")
				return
			}
		}
	}
	if !seen[model.RoleSuperAdmin] {
		writeError(w, http.StatusConflict, "super_admin_required", "super_admin 역할은 삭제할 수 없습니다")
		return
	}
	tx, err := s.repo.Pool().Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "역할 정책을 저장할 수 없습니다")
		return
	}
	defer tx.Rollback(r.Context())
	for _, role := range input.Roles {
		if _, err := tx.Exec(r.Context(), `INSERT INTO roles(name,description,system) VALUES($1,$2,false) ON CONFLICT(name) DO UPDATE SET description=EXCLUDED.description,updated_at=now()`, role.Name, role.Description); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "역할 정책을 저장할 수 없습니다")
			return
		}
		if role.Name != model.RoleSuperAdmin {
			if _, err := tx.Exec(r.Context(), `DELETE FROM role_permissions WHERE role_name=$1`, role.Name); err != nil {
				writeError(w, http.StatusInternalServerError, "storage_error", "역할 정책을 저장할 수 없습니다")
				return
			}
			for _, permission := range role.Permissions {
				if _, err := tx.Exec(r.Context(), `INSERT INTO role_permissions(role_name,permission) VALUES($1,$2)`, role.Name, permission); err != nil {
					writeError(w, http.StatusInternalServerError, "storage_error", "역할 정책을 저장할 수 없습니다")
					return
				}
			}
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "역할 정책을 저장할 수 없습니다")
		return
	}
	s.invalidateRolePermissions(r.Context())
	s.audit(r, "admin.roles.update", "role", "*", true, map[string]int{"count": len(input.Roles)})
	s.adminListRoles(w, r)
}

func (s *Server) adminListAudit(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := pagination(w, r)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	pattern := "%" + escapeLike(strings.ToLower(query)) + "%"
	rows, err := s.repo.Pool().Query(r.Context(), `SELECT a.id,a.actor_id,COALESCE(u.username,''),a.action,a.target_type,a.target_id,a.success,a.ip,a.user_agent,a.detail,a.created_at FROM audit_events a LEFT JOIN users u ON u.id=a.actor_id WHERE $1='' OR lower(COALESCE(u.username,'')) LIKE $2 ESCAPE E'\\' OR lower(a.action) LIKE $2 ESCAPE E'\\' OR lower(a.target_type||':'||a.target_id) LIKE $2 ESCAPE E'\\' ORDER BY a.created_at DESC LIMIT $3 OFFSET $4`, query, pattern, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "감사 로그를 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, actorID, actorUsername, action, targetType, targetID, ip, userAgent string
		var success bool
		var detail json.RawMessage
		var createdAt time.Time
		if rows.Scan(&id, &actorID, &actorUsername, &action, &targetType, &targetID, &success, &ip, &userAgent, &detail, &createdAt) != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "감사 로그를 불러올 수 없습니다")
			return
		}
		items = append(items, map[string]any{"id": id, "actorId": actorID, "actorUsername": actorUsername, "action": action, "targetType": targetType, "targetId": targetID, "target": targetType + ":" + targetID, "success": success, "ip": ip, "userAgent": userAgent, "detail": detail, "createdAt": createdAt})
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (s *Server) adminListKeys(w http.ResponseWriter, r *http.Request) {
	items, err := s.repo.ListAPIKeys(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "API 키 목록을 불러올 수 없습니다")
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, key := range items {
		user, _ := s.repo.UserByID(r.Context(), key.UserID)
		key.TokenHash = ""
		result = append(result, map[string]any{"key": key, "id": key.ID, "userId": key.UserID, "username": user.Username, "name": key.Name, "prefix": key.Prefix, "permissions": key.Permissions, "version": key.Version, "createdAt": key.CreatedAt, "rotatedAt": key.RotatedAt, "lastUsedAt": key.LastUsedAt, "expiresAt": key.ExpiresAt, "revokedAt": key.RevokedAt})
	}
	writeData(w, http.StatusOK, map[string]any{"items": result})
}

func (s *Server) adminUpdateKey(w http.ResponseWriter, r *http.Request) {
	var input keyInput
	if !decodeJSON(w, r, &input) {
		return
	}
	all, err := s.repo.ListAPIKeys(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "키를 불러올 수 없습니다")
		return
	}
	var existing *model.APIKey
	for index := range all {
		if all[index].ID == chi.URLParam(r, "keyID") {
			existing = &all[index]
			break
		}
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "키를 찾을 수 없습니다")
		return
	}
	owner, err := s.repo.UserByID(r.Context(), existing.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "키 소유자를 찾을 수 없습니다")
		return
	}
	ownerPermissions, err := s.repo.PermissionsForRoles(r.Context(), owner.Roles)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "키 권한을 확인할 수 없습니다")
		return
	}
	permissions := uniqueStrings(input.Permissions)
	for _, permission := range permissions {
		if !permissionPattern.MatchString(permission) || !hasPermission(ownerPermissions, permission) || !hasPermission(getPrincipal(r).Permissions, permission) {
			writeError(w, http.StatusForbidden, "scope_escalation", "키 소유자 또는 현재 관리자 권한을 넘을 수 없습니다")
			return
		}
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = existing.Name
	}
	if len(permissions) == 0 {
		permissions = existing.Permissions
	}
	key, err := s.repo.UpdateAPIKey(r.Context(), existing.ID, "", name, permissions)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "키를 변경할 수 없습니다")
		return
	}
	key.TokenHash = ""
	s.audit(r, "admin.key.update", "api_key", key.ID, true, map[string]any{"permissions": permissions})
	writeData(w, http.StatusOK, key)
}

func (s *Server) adminRevokeKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "keyID")
	if err := s.repo.RevokeAPIKey(r.Context(), id, ""); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "활성 키를 찾을 수 없습니다")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "키를 폐기할 수 없습니다")
		return
	}
	s.audit(r, "admin.key.revoke", "api_key", id, true, nil)
	w.WriteHeader(http.StatusNoContent)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return result
}
