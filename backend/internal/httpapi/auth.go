package httpapi

import (
	"context"
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
	"golang.org/x/crypto/bcrypt"
)

var usernamePattern = regexp.MustCompile(`^[\pL\pN][\pL\pN._-]{2,39}$`)
var permissionPattern = regexp.MustCompile(`^(?:\*|[a-z][a-z0-9_.-]*:(?:\*|[a-z][a-z0-9_.-]*))$`)

type loginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.general(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "서비스 설정을 확인할 수 없습니다")
		return
	}
	if !cfg.AllowRegistration {
		writeError(w, http.StatusForbidden, "registration_disabled", "관리자가 회원가입을 허용하지 않았습니다")
		return
	}
	allowed, rateErr := s.allow(r.Context(), "register|"+clientIP(r), 5, 10*time.Minute)
	if rateErr != nil {
		writeError(w, http.StatusServiceUnavailable, "rate_limit_unavailable", "가입 요청 한도를 확인할 수 없습니다")
		return
	}
	if !allowed {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "회원가입 요청이 너무 많습니다")
		return
	}
	var input struct {
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
		Password    string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.TrimSpace(input.Email)
	if !validUsername(input.Username) || !validDisplayName(input.DisplayName) || !validEmail(input.Email) || !validPassword(input.Password) {
		writeError(w, http.StatusBadRequest, "invalid_registration", "아이디, 이름, 이메일 또는 비밀번호 형식이 올바르지 않습니다")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password_error", "비밀번호를 처리할 수 없습니다")
		return
	}
	user := model.User{ID: secure.NewID("usr"), Username: input.Username, DisplayName: input.DisplayName, Email: input.Email, AccountType: "human", Provider: "local", Roles: []string{model.RoleMember}, Active: true}
	_, err = s.repo.Pool().Exec(r.Context(), `INSERT INTO users(id,username,display_name,email,password_hash,account_type,provider,roles,active) VALUES($1,$2,$3,$4,$5,'human','local',$6,true)`, user.ID, user.Username, user.DisplayName, user.Email, string(hash), user.Roles)
	if store.IsConflict(err) {
		writeError(w, http.StatusConflict, "username_taken", "이미 사용 중인 아이디입니다")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "사용자를 만들 수 없습니다")
		return
	}
	user, _ = s.repo.UserByID(r.Context(), user.ID)
	if err := s.issueSession(w, r, user); err != nil {
		writeError(w, http.StatusInternalServerError, "session_failed", "로그인 세션을 만들 수 없습니다")
		return
	}
	permissions, permissionErr := s.repo.PermissionsForRoles(r.Context(), user.Roles)
	if permissionErr != nil {
		writeError(w, http.StatusInternalServerError, "role_error", "사용자 권한을 불러올 수 없습니다")
		return
	}
	r = r.WithContext(withPrincipal(r, principal{User: user, Permissions: permissions}))
	s.audit(r, "auth.register", "user", user.ID, true, nil)
	writeData(w, http.StatusCreated, authView(user, getPrincipal(r).Permissions))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input loginInput
	if !decodeJSON(w, r, &input) {
		return
	}
	username := strings.ToLower(strings.TrimSpace(input.Username))
	key := "login|" + username + "|" + clientIP(r)
	allowedByClient, clientRateErr := s.allow(r.Context(), key, 5, 5*time.Minute)
	allowedByUser, userRateErr := s.allow(r.Context(), "login-user|"+username, 20, 5*time.Minute)
	if clientRateErr != nil || userRateErr != nil {
		writeError(w, http.StatusServiceUnavailable, "rate_limit_unavailable", "로그인 요청 한도를 확인할 수 없습니다")
		return
	}
	if !allowedByClient || !allowedByUser {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "로그인 시도가 너무 많습니다. 잠시 후 다시 시도해 주세요")
		return
	}
	if username == "" || len([]byte(input.Password)) > 72 {
		s.auditAnonymous(r, "auth.login", false)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "아이디 또는 비밀번호가 올바르지 않습니다")
		return
	}
	auth, err := s.repo.UserByUsername(r.Context(), username)
	if err != nil || !auth.Active || auth.Provider != "local" || bcrypt.CompareHashAndPassword([]byte(auth.PasswordHash), []byte(input.Password)) != nil {
		s.auditAnonymous(r, "auth.login", false)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "아이디 또는 비밀번호가 올바르지 않습니다")
		return
	}
	permissions, err := s.repo.PermissionsForRoles(r.Context(), auth.Roles)
	if err != nil || s.issueSession(w, r, auth.User) != nil {
		writeError(w, http.StatusInternalServerError, "session_failed", "로그인 세션을 만들 수 없습니다")
		return
	}
	p := principal{User: auth.User, Permissions: permissions}
	r = r.WithContext(withPrincipal(r, p))
	s.audit(r, "auth.login", "user", auth.ID, true, nil)
	writeData(w, http.StatusOK, authView(auth.User, permissions))
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, user model.User) error {
	cfg, err := s.general(r)
	if err != nil {
		return err
	}
	token, err := secure.RandomToken(32)
	if err != nil {
		return err
	}
	csrf, err := secure.RandomToken(24)
	if err != nil {
		return err
	}
	token = "ms_" + token
	duration := time.Duration(cfg.SessionMinutes) * time.Minute
	session := model.Session{ID: secure.NewID("ses"), UserID: user.ID, TokenHash: s.secrets.HashToken(token), CSRFHash: s.secrets.HashToken(csrf), ExpiresAt: time.Now().UTC().Add(duration)}
	if err := s.repo.CreateSession(r.Context(), session); err != nil {
		return err
	}
	secureCookie := isHTTPS(r)
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secureCookie, SameSite: http.SameSiteLaxMode, MaxAge: int(duration.Seconds()), Expires: session.ExpiresAt})
	http.SetCookie(w, &http.Cookie{Name: CSRFCookie, Value: csrf, Path: "/", HttpOnly: false, Secure: secureCookie, SameSite: http.SameSiteLaxMode, MaxAge: int(duration.Seconds()), Expires: session.ExpiresAt})
	return nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	writeData(w, http.StatusOK, authView(p.User, p.Permissions))
}

func authView(user model.User, permissions []string) map[string]any {
	return map[string]any{"user": profileView(user), "permissions": permissions, "roles": user.Roles}
}

func profileView(user model.User) map[string]any {
	avatarURL := ""
	if user.AvatarID != "" {
		avatarURL = "/api/v1/media/" + user.AvatarID
	}
	return map[string]any{"id": user.ID, "username": user.Username, "displayName": user.DisplayName, "email": user.Email, "bio": user.Bio, "avatarId": user.AvatarID, "avatarUrl": avatarURL, "accountType": user.AccountType, "provider": user.Provider, "roles": user.Roles, "active": user.Active, "createdAt": user.CreatedAt, "updatedAt": user.UpdatedAt}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		_ = s.repo.DeleteSession(r.Context(), s.secrets.HashToken(cookie.Value))
	}
	clearAuthCookies(w, r)
	s.audit(r, "auth.logout", "user", getPrincipal(r).User.ID, true, nil)
	w.WriteHeader(http.StatusNoContent)
}

func clearAuthCookies(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{SessionCookie, CSRFCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: name == SessionCookie, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	}
}

func (s *Server) auditAnonymous(r *http.Request, action string, success bool) {
	event := store.AuditEvent{ID: secure.NewID("aud"), Action: action, Success: success, IP: clientIP(r), UserAgent: r.UserAgent(), CreatedAt: time.Now().UTC(), Detail: auditDetail(r, nil)}
	_ = s.repo.AddAudit(r.Context(), event)
}

func withPrincipal(r *http.Request, p principal) context.Context {
	return context.WithValue(r.Context(), principalKey{}, p)
}

func (s *Server) requireBrowserSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if getPrincipal(r).APIKey {
			writeError(w, http.StatusForbidden, "session_required", "이 작업은 브라우저 세션 인증이 필요합니다")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return requestNetworkInfo(r).ForwardedProto == "https" || browserOriginScheme(r) == "https"
}

func validUsername(value string) bool {
	return utf8.ValidString(value) && usernamePattern.MatchString(value)
}

func validDisplayName(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 80 && !strings.ContainsAny(value, "\x00\r\n")
}

func validEmail(value string) bool {
	return utf8.ValidString(value) && len(value) <= 254 && !strings.ContainsAny(value, "\x00\r\n") && (value == "" || strings.Count(value, "@") == 1)
}

func validPassword(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) >= 12 && len([]byte(value)) <= 72
}

func (s *Server) myProfile(w http.ResponseWriter, r *http.Request) {
	s.getProfileByUser(w, r, getPrincipal(r).User.ID)
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	var input struct {
		DisplayName *string `json:"displayName"`
		Email       *string `json:"email"`
		Bio         *string `json:"bio"`
		AvatarID    *string `json:"avatarId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.DisplayName != nil {
		value := strings.TrimSpace(*input.DisplayName)
		if !validDisplayName(value) {
			writeError(w, http.StatusBadRequest, "invalid_profile", "프로필 이름 형식이 올바르지 않습니다")
			return
		}
		input.DisplayName = &value
	}
	if input.Email != nil {
		value := strings.TrimSpace(*input.Email)
		if !validEmail(value) {
			writeError(w, http.StatusBadRequest, "invalid_profile", "이메일 형식이 올바르지 않습니다")
			return
		}
		input.Email = &value
	}
	if input.Bio != nil {
		value := strings.TrimSpace(*input.Bio)
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 500 || strings.ContainsRune(value, '\x00') {
			writeError(w, http.StatusBadRequest, "invalid_profile", "프로필 소개 형식이 올바르지 않습니다")
			return
		}
		input.Bio = &value
	}
	if input.AvatarID != nil {
		value := strings.TrimSpace(*input.AvatarID)
		input.AvatarID = &value
	}
	if input.AvatarID != nil && *input.AvatarID != "" {
		var exists bool
		if err := s.repo.Pool().QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM media_assets WHERE id=$1 AND owner_id=$2 AND mime_type LIKE 'image/%')`, *input.AvatarID, p.User.ID).Scan(&exists); err != nil || !exists {
			writeError(w, http.StatusBadRequest, "invalid_avatar", "본인이 업로드한 이미지만 프로필 사진으로 사용할 수 있습니다")
			return
		}
	}
	user, err := scanUserRow(s.repo.Pool().QueryRow(r.Context(), `UPDATE users SET display_name=COALESCE($2,display_name),email=COALESCE($3,email),bio=COALESCE($4,bio),avatar_id=COALESCE($5,avatar_id),updated_at=now() WHERE id=$1 RETURNING `+userSelectColumns, p.User.ID, input.DisplayName, input.Email, input.Bio, input.AvatarID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "프로필을 저장할 수 없습니다")
		return
	}
	s.audit(r, "profile.update", "user", p.User.ID, true, nil)
	writeData(w, http.StatusOK, profileView(user))
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	if p.User.Provider != "local" {
		writeError(w, http.StatusConflict, "oidc_password", "SSO 사용자의 비밀번호는 Keycloak에서 변경해야 합니다")
		return
	}
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !validPassword(input.NewPassword) || input.CurrentPassword == input.NewPassword {
		writeError(w, http.StatusBadRequest, "invalid_password", "새 비밀번호는 기존 비밀번호와 다르고 12자 이상이어야 합니다")
		return
	}
	auth, err := s.repo.UserByUsername(r.Context(), p.User.Username)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(auth.PasswordHash), []byte(input.CurrentPassword)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid_current_password", "현재 비밀번호가 올바르지 않습니다")
		return
	}
	hash, hashErr := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if hashErr != nil {
		writeError(w, http.StatusInternalServerError, "password_error", "비밀번호를 처리할 수 없습니다")
		return
	}
	if err := s.repo.UpdatePassword(r.Context(), p.User.ID, string(hash)); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "비밀번호를 변경할 수 없습니다")
		return
	}
	_ = s.repo.DeleteUserSessions(r.Context(), p.User.ID)
	clearAuthCookies(w, r)
	s.audit(r, "profile.password.update", "user", p.User.ID, true, nil)
	w.WriteHeader(http.StatusNoContent)
}

type keyInput struct {
	Name        string     `json:"name"`
	Permissions []string   `json:"permissions"`
	ExpiresAt   *time.Time `json:"expiresAt"`
}

func (s *Server) listMyKeys(w http.ResponseWriter, r *http.Request) {
	items, err := s.repo.ListAPIKeys(r.Context(), getPrincipal(r).User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "키 목록을 불러올 수 없습니다")
		return
	}
	for index := range items {
		items[index].TokenHash = ""
	}
	writeData(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createMyKey(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	var input keyInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 120 || strings.ContainsAny(input.Name, "\x00\r\n") {
		writeError(w, http.StatusBadRequest, "invalid_name", "키 이름은 줄바꿈 없이 1~120자여야 합니다")
		return
	}
	if len(input.Permissions) == 0 {
		input.Permissions = []string{"posts:read", "mcp:use"}
	}
	permissions, err := normalizeKeyPermissions(p, input.Permissions)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid_scope", err.Error())
		return
	}
	if input.ExpiresAt != nil && input.ExpiresAt.Before(time.Now().Add(time.Minute)) {
		writeError(w, http.StatusBadRequest, "invalid_expiry", "만료 시각은 현재보다 뒤여야 합니다")
		return
	}
	token, err := newAPIKeyToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "key_failed", "키를 만들 수 없습니다")
		return
	}
	key := model.APIKey{ID: secure.NewID("key"), UserID: p.User.ID, Name: input.Name, Prefix: token[:12], TokenHash: s.secrets.HashToken(token), Permissions: permissions, ExpiresAt: input.ExpiresAt}
	key, err = s.repo.CreateAPIKey(r.Context(), key, p.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "키를 저장할 수 없습니다")
		return
	}
	s.audit(r, "key.create", "api_key", key.ID, true, map[string]any{"permissions": permissions})
	writeData(w, http.StatusCreated, keySecretView(key, token))
}

func (s *Server) updateMyKey(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	var input keyInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	permissions, err := normalizeKeyPermissions(p, input.Permissions)
	if input.Name == "" || err != nil {
		writeError(w, http.StatusBadRequest, "invalid_key", "키 이름 또는 권한이 올바르지 않습니다")
		return
	}
	key, err := s.repo.UpdateAPIKey(r.Context(), chi.URLParam(r, "keyID"), p.User.ID, input.Name, permissions)
	if store.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "not_found", "키를 찾을 수 없습니다")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "키를 변경할 수 없습니다")
		return
	}
	key.TokenHash = ""
	s.audit(r, "key.update", "api_key", key.ID, true, map[string]any{"permissions": permissions})
	writeData(w, http.StatusOK, key)
}

func (s *Server) rotateMyKey(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	token, err := newAPIKeyToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "key_failed", "키를 회전할 수 없습니다")
		return
	}
	key, err := s.repo.RotateAPIKey(r.Context(), chi.URLParam(r, "keyID"), p.User.ID, s.secrets.HashToken(token), token[:12], p.User.ID)
	if store.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "not_found", "활성 키를 찾을 수 없습니다")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "키를 회전할 수 없습니다")
		return
	}
	s.audit(r, "key.rotate", "api_key", key.ID, true, map[string]int{"version": key.Version})
	writeData(w, http.StatusOK, keySecretView(key, token))
}

func (s *Server) revokeMyKey(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	id := chi.URLParam(r, "keyID")
	if err := s.repo.RevokeAPIKey(r.Context(), id, p.User.ID); store.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "not_found", "활성 키를 찾을 수 없습니다")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "키를 폐기할 수 없습니다")
		return
	}
	s.audit(r, "key.revoke", "api_key", id, true, nil)
	w.WriteHeader(http.StatusNoContent)
}

func normalizeKeyPermissions(p principal, values []string) ([]string, error) {
	if len(values) == 0 || len(values) > 128 {
		return nil, errors.New("키 권한은 1~128개여야 합니다")
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !permissionPattern.MatchString(value) || seen[value] || !hasPermission(p.Permissions, value) {
			return nil, errors.New("현재 사용자 권한을 넘거나 형식이 올바르지 않은 키 권한입니다")
		}
		seen[value] = true
		result = append(result, value)
	}
	slices.Sort(result)
	return result, nil
}

func newAPIKeyToken() (string, error) {
	value, err := secure.RandomToken(32)
	return "mk_" + value, err
}

func keySecretView(key model.APIKey, token string) map[string]any {
	key.TokenHash = ""
	return map[string]any{"key": key, "token": token, "secret": token}
}

func (s *Server) getProfileByUser(w http.ResponseWriter, r *http.Request, userID string) {
	// Implemented in social.go; keeping the call here makes profile ownership
	// explicit without duplicating the social graph counters.
	s.writeProfile(w, r, userID)
}

// pgx.Row is deliberately accepted so profile/admin queries can share the
// same scanner without leaking password or OIDC identity columns.
const userSelectColumns = `id,username,display_name,email,bio,avatar_id,account_type,provider,roles,active,created_at,updated_at`

type rowScanner interface{ Scan(...any) error }

func scanUserRow(row rowScanner) (model.User, error) {
	var user model.User
	err := row.Scan(&user.ID, &user.Username, &user.DisplayName, &user.Email, &user.Bio, &user.AvatarID, &user.AccountType, &user.Provider, &user.Roles, &user.Active, &user.CreatedAt, &user.UpdatedAt)
	return user, err
}
