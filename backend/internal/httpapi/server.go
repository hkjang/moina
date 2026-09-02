package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hkjang/moina/backend/internal/event"
	mediastore "github.com/hkjang/moina/backend/internal/media"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/observability"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
	"github.com/jackc/pgx/v5"
)

const (
	SessionCookie = "moina_session"
	CSRFCookie    = "moina_csrf"
	OIDCCookie    = "moina_oidc_flow"
	maxBodyBytes  = 4 << 20
	// gzip level 5 is the usual balance: most of the ratio of level 9 for a
	// fraction of the CPU, which matters because every JSON response pays it.
	compressionLevel = 5
	mediaUploadPath  = "/api/v1/media"
	// An upload may legitimately run for minutes; the administrator ceiling is
	// 50 MiB, which a slow mobile link does not move in 30 seconds.
	requestBodyReadTimeout = 30 * time.Second
	uploadBodyReadTimeout  = 15 * time.Minute
	defaultStaticRoot      = "/app/web/dist"
)

type principal struct {
	User        model.User
	Permissions []string
	APIKey      bool
	APIKeyID    string
	CSRFHash    string
}

type principalKey struct{}

type attempt struct {
	Count   int
	Started time.Time
}

type Server struct {
	repo            *store.Store
	secrets         *secure.Manager
	version         string
	startedAt       time.Time
	client          *http.Client
	hub             *notificationHub
	metrics         *observability.Registry
	outbox          *event.Repository
	media           mediastore.MediaStore
	rateMu          sync.Mutex
	rates           map[string]*attempt
	networkMu       sync.Mutex
	networkCache    networkConfig
	networkLoadedAt time.Time
	staticRoot      string
}

func New(repo *store.Store, secrets *secure.Manager, version string) *Server {
	server := &Server{
		repo: repo, secrets: secrets, version: version, startedAt: time.Now().UTC(),
		client: &http.Client{}, hub: newNotificationHub(), metrics: observability.NewRegistry(), rates: make(map[string]*attempt), staticRoot: defaultStaticRoot,
	}
	if repo != nil {
		server.outbox = event.NewRepository(repo.Pool())
		server.media = mediastore.NewPostgreSQLStore(repo.Pool(), 0)
	}
	return server
}

// SetHTTPClient installs the client used for OIDC discovery and AI provider
// calls. The executable uses it to add the optional fixed-path private CA.
func (s *Server) SetHTTPClient(client *http.Client) {
	if client != nil {
		s.client = client
	}
}

// SetObservability installs the process-wide registry also used by the pgx
// tracer. New supplies a private registry so tests and embedded callers remain
// safe when this method is not called.
func (s *Server) SetObservability(registry *observability.Registry) {
	if registry != nil {
		s.metrics = registry
	}
}

func (s *Server) Handler() http.Handler {
	router := chi.NewRouter()
	router.Use(s.resolveRequestNetwork, s.bodyReadDeadline, compressResponses, observability.HTTPMiddleware(slog.Default()), s.recoverJSON, s.securityHeaders, s.verifyOrigin)
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	router.Get("/readyz", s.ready)
	router.Get("/metrics", s.metricsEndpoint)
	router.Route("/api/v1", func(api chi.Router) {
		api.Get("/version", s.versionInfo)
		api.Post("/auth/login", s.login)
		api.Post("/auth/register", s.register)
		api.Get("/auth/oidc/status", s.oidcStatus)
		api.Get("/auth/oidc/login", s.oidcLogin)
		api.Get("/auth/oidc/callback", s.oidcCallback)
		api.Group(func(auth chi.Router) {
			auth.Use(s.authenticate)
			auth.Get("/auth/me", s.me)
			auth.Post("/auth/logout", s.logout)

			auth.With(s.requirePermission("posts:read"), s.metrics.FlowMiddleware).Get("/feed", s.feed)
			auth.With(s.requirePermission("posts:read")).Get("/posts", s.listPosts)
			auth.With(s.requirePermission("posts:write")).Post("/posts", s.createPost)
			auth.With(s.requirePermission("posts:read")).Get("/posts/{postID}", s.getPost)
			auth.With(s.requirePermission("posts:write")).Patch("/posts/{postID}", s.updatePost)
			auth.With(s.requirePermission("posts:write")).Delete("/posts/{postID}", s.deletePost)
			auth.With(s.requirePermission("posts:read")).Get("/posts/{postID}/replies", s.listReplies)
			auth.With(s.requirePermission("social:write")).Post("/posts/{postID}/reactions", s.putReaction)
			auth.With(s.requirePermission("social:write")).Delete("/posts/{postID}/reactions/{reaction}", s.deleteReaction)
			auth.With(s.requirePermission("social:write")).Delete("/posts/{postID}/reactions", s.deleteReaction)
			auth.With(s.requirePermission("social:write")).Post("/posts/{postID}/bookmark", s.putBookmark)
			auth.With(s.requirePermission("social:write")).Delete("/posts/{postID}/bookmark", s.deleteBookmark)
			auth.With(s.requirePermission("posts:write")).Post("/posts/{postID}/remoin", s.remoinPost)
			auth.With(s.requirePermission("posts:write")).Delete("/posts/{postID}/remoin", s.deleteRemoin)

			auth.With(s.requirePermission("posts:read")).Get("/users/{username}", s.getProfile)
			auth.With(s.requirePermission("social:write")).Post("/links/{userID}", s.followUser)
			auth.With(s.requirePermission("social:write")).Delete("/links/{userID}", s.unfollowUser)
			auth.With(s.requirePermission("social:write")).Post("/users/{userID}/block", s.blockUser)
			auth.With(s.requirePermission("social:write")).Delete("/users/{userID}/block", s.unblockUser)
			auth.With(s.requirePermission("social:write")).Post("/users/{userID}/mute", s.muteUser)
			auth.With(s.requirePermission("social:write")).Delete("/users/{userID}/mute", s.unmuteUser)

			auth.With(s.requireBrowserSession).Get("/profile", s.myProfile)
			auth.With(s.requireBrowserSession).Patch("/profile", s.updateProfile)
			auth.With(s.requireBrowserSession).Get("/profile/preferences", s.getPreferences)
			auth.With(s.requireBrowserSession).Put("/profile/preferences", s.putPreferences)
			auth.With(s.requireBrowserSession).Post("/profile/password", s.changePassword)
			auth.With(s.requireBrowserSession).Get("/profile/keys", s.listMyKeys)
			auth.With(s.requireBrowserSession).Post("/profile/keys", s.createMyKey)
			auth.With(s.requireBrowserSession).Patch("/profile/keys/{keyID}", s.updateMyKey)
			auth.With(s.requireBrowserSession).Post("/profile/keys/{keyID}/rotate", s.rotateMyKey)
			auth.With(s.requireBrowserSession).Delete("/profile/keys/{keyID}", s.revokeMyKey)

			auth.With(s.requirePermission("posts:read")).Get("/topics", s.listTopics)
			auth.With(s.requirePermission("posts:read")).Get("/topics/{slug}", s.getTopic)
			auth.With(s.requirePermission("social:write")).Post("/topics/{slug}/follow", s.followTopic)
			auth.With(s.requirePermission("social:write")).Delete("/topics/{slug}/follow", s.unfollowTopic)
			auth.With(s.requirePermission("posts:read"), s.metrics.SearchMiddleware).Get("/search", s.search)

			auth.With(s.requireBrowserSession).Get("/notifications", s.listNotifications)
			auth.With(s.requireBrowserSession).Post("/notifications/read", s.readNotifications)
			auth.With(s.requireBrowserSession).Get("/notifications/email/status", s.notificationEmailStatus)
			auth.With(s.requireBrowserSession).Get("/ws/notifications", s.notificationsWebSocket)

			auth.With(s.requirePermission("posts:read")).Get("/moims", s.listMoims)
			auth.With(s.requirePermission("social:write")).Post("/moims", s.createMoim)
			auth.With(s.requirePermission("posts:read")).Get("/moims/{slug}", s.getMoim)
			auth.With(s.requirePermission("social:write")).Post("/moims/{slug}/join", s.joinMoim)
			auth.With(s.requirePermission("social:write")).Delete("/moims/{slug}/join", s.leaveMoim)
			auth.With(s.requirePermission("social:write")).Post("/moims/{slug}/members", s.joinMoim)
			auth.With(s.requirePermission("social:write")).Delete("/moims/{slug}/members", s.leaveMoim)

			// Browser sessions may manage personal profile media even when a
			// custom role does not grant post permissions. API keys retain the
			// explicit post scopes used by media automation.
			auth.With(s.requireBrowserSessionOrPermission("posts:write")).Post("/media", s.uploadMedia)
			auth.With(s.requireBrowserSessionOrPermission("posts:write")).Get("/media/config", s.mediaConfigStatus)
			auth.Get("/media/{mediaID}", s.getMedia)
			auth.With(s.requireBrowserSessionOrPermission("posts:write")).Delete("/media/{mediaID}", s.deleteMedia)
			auth.With(s.requirePermission("social:write")).Post("/reports", s.createReport)

			auth.With(s.requirePermission("posts:read")).Get("/workflow/status", s.workflowStatus)
			auth.With(s.requireBrowserSession, s.requirePermission("approvals:review")).Get("/approvals", s.listApprovals)
			auth.With(s.requireBrowserSession, s.requirePermission("approvals:review")).Post("/approvals/{approvalID}/approve", s.approve)
			auth.With(s.requireBrowserSession, s.requirePermission("approvals:review")).Post("/approvals/{approvalID}/reject", s.reject)

			auth.With(s.requirePermission("ai:use")).Get("/ai/status", s.aiStatus)
			auth.With(s.requirePermission("ai:use")).Post("/ai/chat", s.aiChat)
			auth.With(s.requirePermission("mcp:use")).Method(http.MethodPost, "/mcp", s.mcpHandler())
			auth.With(s.requirePermission("mcp:use")).Method(http.MethodGet, "/mcp", s.mcpHandler())

			// Brand-language aliases remain intentionally thin and share handlers.
			auth.With(s.requirePermission("posts:read"), s.metrics.FlowMiddleware).Get("/flows/{mode}", s.feedAlias)
			auth.With(s.requirePermission("posts:read")).Get("/moins", s.listPosts)
			auth.With(s.requirePermission("posts:write")).Post("/moins", s.createPost)
			auth.With(s.requirePermission("posts:read")).Get("/moins/{postID}", s.getPost)
			auth.With(s.requirePermission("posts:read")).Get("/profiles/{username}", s.getProfile)
			auth.With(s.requireBrowserSession).Get("/ws", s.notificationsWebSocket)

			auth.Route("/admin", func(admin chi.Router) {
				admin.Use(s.requireBrowserSession)
				admin.Use(s.requirePermission("admin:access"))
				admin.With(s.requirePermission("audit:read")).Get("/stats", s.adminStats)
				admin.With(s.requirePermission("users:manage")).Get("/users", s.adminListUsers)
				admin.With(s.requirePermission("users:manage")).Post("/users", s.adminCreateUser)
				admin.With(s.requirePermission("users:manage")).Patch("/users/{userID}", s.adminUpdateUser)
				admin.With(s.requirePermission("users:manage")).Post("/users/{userID}/password", s.adminResetPassword)
				admin.With(s.requirePermission("posts:manage")).Get("/posts", s.adminListPosts)
				admin.With(s.requirePermission("posts:manage")).Patch("/posts/{postID}", s.adminUpdatePost)
				admin.With(s.requirePermission("posts:manage")).Delete("/posts/{postID}", s.adminDeletePost)
				admin.With(s.requirePermission("moderation:manage")).Get("/reports", s.adminListReports)
				admin.With(s.requirePermission("moderation:manage")).Patch("/reports/{reportID}", s.adminResolveReport)
				admin.With(s.requirePermission("moderation:manage")).Post("/reports/{reportID}/resolve", s.adminResolveReportAlias)
				admin.With(s.requirePermission("moderation:manage")).Post("/reports/{reportID}/reject", s.adminResolveReportAlias)
				admin.With(s.requirePermission("roles:manage")).Get("/roles", s.adminListRoles)
				admin.With(s.requirePermission("roles:manage")).Put("/roles", s.adminPutRoles)
				admin.With(s.requirePermission("settings:manage")).Get("/settings", s.adminListSettings)
				admin.With(s.requirePermission("settings:manage")).Put("/settings/{settingKey}", s.adminPutSetting)
				admin.With(s.requirePermission("settings:manage")).Get("/oidc", s.adminGetOIDC)
				admin.With(s.requirePermission("settings:manage")).Put("/oidc", s.adminPutOIDC)
				admin.With(s.requirePermission("settings:manage")).Post("/oidc/test", s.adminTestOIDC)
				admin.With(s.requirePermission("settings:manage")).Get("/ai", s.adminGetAI)
				admin.With(s.requirePermission("settings:manage")).Put("/ai", s.adminPutAI)
				admin.With(s.requirePermission("settings:manage")).Post("/ai/test", s.adminTestAI)
				admin.With(s.requirePermission("settings:manage")).Get("/smtp", s.adminGetSMTP)
				admin.With(s.requirePermission("settings:manage")).Put("/smtp", s.adminPutSMTP)
				admin.With(s.requirePermission("settings:manage")).Post("/smtp/test", s.adminTestSMTP)
				admin.With(s.requirePermission("settings:manage")).Get("/workflow", s.adminGetWorkflow)
				admin.With(s.requirePermission("settings:manage")).Put("/workflow", s.adminPutWorkflow)
				admin.With(s.requirePermission("audit:read")).Get("/audit", s.adminListAudit)
				admin.With(s.requirePermission("audit:read")).Get("/outbox", s.adminListOutbox)
				admin.With(s.requirePermission("outbox:manage")).Post("/outbox/{eventID}/retry", s.adminRetryOutbox)
				admin.With(s.requirePermission("keys:manage")).Get("/keys", s.adminListKeys)
				admin.With(s.requirePermission("keys:manage")).Patch("/keys/{keyID}", s.adminUpdateKey)
				admin.With(s.requirePermission("keys:manage")).Delete("/keys/{keyID}", s.adminRevokeKey)
			})
		})
	})
	router.Group(func(root chi.Router) {
		root.Use(s.authenticate)
		root.With(s.requirePermission("mcp:use")).Method(http.MethodPost, "/mcp", s.mcpHandler())
		root.With(s.requirePermission("mcp:use")).Method(http.MethodGet, "/mcp", s.mcpHandler())
	})
	router.NotFound(s.serveSPA)
	return router
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if s.repo == nil || s.repo.Ping(ctx) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "데이터베이스에 연결할 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) versionInfo(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, map[string]any{"name": "moina", "service": "moina", "version": s.version, "startedAt": s.startedAt})
}

func (s *Server) metricsEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.repo != nil {
		s.metrics.ObserveDBPool(s.repo.Pool().Stat())
	}
	s.metrics.ServeHTTP(w, r)
}

// compressResponses encodes the text payloads that dominate a cold load: the
// SPA bundle, its stylesheet, and every JSON response. chi's default type list
// deliberately excludes text/event-stream and every media type, so AI streaming
// still flushes token by token and video keeps its byte exact Range responses.
func compressResponses(next http.Handler) http.Handler {
	compressed := middleware.Compress(compressionLevel)(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A range response describes an offset into the identity encoding, so
		// compressing it would make Content-Range describe the wrong bytes.
		if r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		compressed.ServeHTTP(w, r)
	})
}

// bodyReadDeadline replaces the process wide http.Server.ReadTimeout, which
// could not tell a 4 KiB JSON body from a 50 MiB upload and cut both off at the
// same 30 seconds: a trickled upload died at exactly that mark. Slow header
// attacks stay covered by ReadHeaderTimeout, and each request body now gets a
// budget matching what it legitimately needs.
//
// The WebSocket case is belt and braces. net/http already clears both deadlines
// when a handler hijacks the connection, so a notification stream was never at
// risk; skipping the upgrade keeps that true no matter where this runs.
func (s *Server) bodyReadDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetReadDeadline(bodyReadDeadlineAt(r, time.Now())); err != nil && !errors.Is(err, http.ErrNotSupported) {
			observability.Logger(r.Context()).WarnContext(r.Context(), "요청 본문 읽기 기한 설정 실패", "error", err)
		}
		next.ServeHTTP(w, r)
	})
}

// bodyReadDeadlineAt returns the zero time for a connection that must outlive
// any single request body.
func bodyReadDeadlineAt(r *http.Request, now time.Time) time.Time {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return time.Time{}
	}
	if r.Method == http.MethodPost && (r.URL.Path == mediaUploadPath || strings.HasPrefix(r.URL.Path, mediaUploadPath+"/")) {
		return now.Add(uploadBodyReadTimeout)
	}
	return now.Add(requestBodyReadTimeout)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", observability.RequestID(r.Context()))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				observability.Logger(r.Context()).ErrorContext(r.Context(), "HTTP panic 복구", "panic", value, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal_error", "요청 처리 중 내부 오류가 발생했습니다")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) verifyOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static files and SPA documents do not cross an authenticated boundary.
		// In particular, browsers send Origin for same-origin ES module requests;
		// applying the API policy here would block /assets/*.js behind a TLS
		// terminating proxy before an administrator can configure that proxy.
		if !originProtectedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			// Native/API clients can omit Origin. Browser requests that include it,
			// including Bearer-authenticated MCP and WebSocket handshakes, are always
			// checked below before credentials are processed.
			next.ServeHTTP(w, r)
			return
		}
		parsed, err := url.Parse(origin)
		if browserOriginScheme(r) != "" {
			// Fetch Metadata describes the browser-visible request relationship and
			// is not writable by page JavaScript. It remains accurate when an
			// unconfigured reverse proxy rewrites scheme or Host, while cross-site
			// and same-site cross-origin requests are still rejected. Non-browser
			// API clients can already omit Origin and follow the branch above.
			next.ServeHTTP(w, r)
			return
		}
		expectedScheme := "http"
		if isHTTPS(r) {
			expectedScheme = "https"
		}
		if err != nil || !strings.EqualFold(parsed.Scheme, expectedScheme) || !sameOriginHost(parsed.Host, r.Host, expectedScheme) {
			writeError(w, http.StatusForbidden, "invalid_origin", "허용되지 않은 요청 출처입니다")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originProtectedPath(path string) bool {
	return path == "/mcp" || path == "/api/v1" || strings.HasPrefix(path, "/api/v1/")
}

func browserOriginScheme(r *http.Request) string {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "same-origin") {
		return ""
	}
	origin, err := url.Parse(strings.TrimSpace(r.Header.Get("Origin")))
	if err != nil || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return ""
	}
	if origin.Scheme == "http" || origin.Scheme == "https" {
		return origin.Scheme
	}
	return ""
}

func sameOriginHost(a, b, scheme string) bool {
	return strings.EqualFold(normalizeOriginHost(a, scheme), normalizeOriginHost(b, scheme))
}

func normalizeOriginHost(value, scheme string) string {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		unbracketed := strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") && net.ParseIP(unbracketed) != nil {
			return unbracketed
		}
		return value
	}
	if strings.EqualFold(scheme, "http") && port == "80" || strings.EqualFold(scheme, "https") && port == "443" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p principal
		var err error
		authorization := r.Header.Get("Authorization")
		if strings.HasPrefix(authorization, "Bearer mk_") {
			token := strings.TrimPrefix(authorization, "Bearer ")
			var key model.APIKey
			p.User, key, err = s.repo.APIKeyUser(r.Context(), s.secrets.HashToken(token))
			p.APIKey = true
			p.APIKeyID = key.ID
			if err == nil {
				permissions, permissionErr := s.repo.PermissionsForRoles(r.Context(), p.User.Roles)
				if permissionErr != nil {
					err = permissionErr
				} else {
					p.Permissions = intersectPermissions(permissions, key.Permissions)
				}
			}
		} else {
			cookie, cookieErr := r.Cookie(SessionCookie)
			if cookieErr != nil {
				err = cookieErr
			} else {
				p.User, p.CSRFHash, err = s.repo.SessionUser(r.Context(), s.secrets.HashToken(cookie.Value))
				if err == nil {
					p.Permissions, err = s.repo.PermissionsForRoles(r.Context(), p.User.Roles)
				}
			}
		}
		if err != nil || !p.User.Active {
			writeError(w, http.StatusUnauthorized, "unauthorized", "인증이 필요합니다")
			return
		}
		if p.APIKey {
			cfg, settingErr := s.apiSettings(r)
			if settingErr != nil || !cfg.Enabled {
				writeError(w, http.StatusServiceUnavailable, "api_disabled", "관리자가 API 키 접근을 비활성화했습니다")
				return
			}
			allowed, rateErr := s.allow(r.Context(), "api-key|"+p.APIKeyID, cfg.RateLimitPerMinute, time.Minute)
			if rateErr != nil {
				writeError(w, http.StatusServiceUnavailable, "rate_limit_unavailable", "요청 한도 정책을 확인할 수 없습니다")
				return
			}
			if !allowed {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, "rate_limited", "API 요청 한도를 초과했습니다")
				return
			}
		}
		if !p.APIKey && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			provided := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
			providedHash := s.secrets.HashToken(provided)
			if provided == "" || subtle.ConstantTimeCompare([]byte(providedHash), []byte(p.CSRFHash)) != 1 {
				writeError(w, http.StatusForbidden, "invalid_csrf", "CSRF 토큰이 없거나 올바르지 않습니다")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func (s *Server) requirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !hasPermission(getPrincipal(r).Permissions, permission) {
				writeError(w, http.StatusForbidden, "forbidden", "이 작업을 수행할 권한이 없습니다")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) requireBrowserSessionOrPermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := getPrincipal(r)
			if principal.APIKey && !hasPermission(principal.Permissions, permission) {
				writeError(w, http.StatusForbidden, "forbidden", "이 작업을 수행할 권한이 없습니다")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func getPrincipal(r *http.Request) principal {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p
}

func hasPermission(permissions []string, want string) bool {
	for _, permission := range permissions {
		if permission == "*" || permission == want || strings.HasSuffix(permission, ":*") && strings.HasPrefix(want, strings.TrimSuffix(permission, "*")) {
			return true
		}
	}
	return false
}

func hasRole(user model.User, roles ...string) bool {
	for _, role := range roles {
		if slices.Contains(user.Roles, model.RoleSuperAdmin) || slices.Contains(user.Roles, role) {
			return true
		}
	}
	return false
}

func intersectPermissions(a, b []string) []string {
	if slices.Contains(a, "*") {
		return append([]string(nil), b...)
	}
	result := make([]string, 0, len(b))
	for _, permission := range b {
		if hasPermission(a, permission) {
			result = append(result, permission)
		}
	}
	slices.Sort(result)
	return result
}

func (s *Server) allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if limit < 1 || window < time.Second {
		return false, errors.New("invalid rate limit")
	}
	if s.repo == nil {
		return s.allowMemory(key, limit, window), nil
	}
	seconds := int(window.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	hash := sha256.Sum256([]byte(key))
	var count int
	err := s.repo.Pool().QueryRow(ctx, `INSERT INTO rate_limit_buckets(key_hash,window_seconds,request_count,started_at,expires_at)
		VALUES($1,$2::integer,1,statement_timestamp(),statement_timestamp()+make_interval(secs=>($2::integer)::double precision))
		ON CONFLICT(key_hash,window_seconds) DO UPDATE SET
			request_count=CASE WHEN rate_limit_buckets.expires_at<=statement_timestamp() THEN 1 ELSE rate_limit_buckets.request_count+1 END,
			started_at=CASE WHEN rate_limit_buckets.expires_at<=statement_timestamp() THEN statement_timestamp() ELSE rate_limit_buckets.started_at END,
			expires_at=CASE WHEN rate_limit_buckets.expires_at<=statement_timestamp() THEN statement_timestamp()+make_interval(secs=>($2::integer)::double precision) ELSE rate_limit_buckets.expires_at END
		WHERE rate_limit_buckets.expires_at<=statement_timestamp() OR rate_limit_buckets.request_count<$3::integer
		RETURNING request_count`, fmt.Sprintf("%x", hash[:]), seconds, limit).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return count <= limit, nil
}

func (s *Server) allowMemory(key string, limit int, window time.Duration) bool {
	now := time.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	for candidate, entry := range s.rates {
		if now.Sub(entry.Started) > window {
			delete(s.rates, candidate)
		}
	}
	entry := s.rates[key]
	if entry == nil {
		s.rates[key] = &attempt{Count: 1, Started: now}
		return true
	}
	if now.Sub(entry.Started) > window {
		entry.Count, entry.Started = 1, now
		return true
	}
	if entry.Count >= limit {
		return false
	}
	entry.Count++
	return true
}

func writeData(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": value})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

func writeErrorDetails(w http.ResponseWriter, status int, code, message string, details any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message, "details": details})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "요청 형식이 올바르지 않습니다")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "JSON 값은 하나만 허용됩니다")
		return false
	}
	return true
}

func pagination(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if r.URL.Query().Get("offset") == "" {
		offset, _ = strconv.Atoi(r.URL.Query().Get("cursor"))
	}
	if limit < 1 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 || offset > 1_000_000 {
		offset = 0
	}
	return limit, offset
}

func (s *Server) audit(r *http.Request, action, targetType, targetID string, success bool, detail any) {
	raw := auditDetail(r, detail)
	event := store.AuditEvent{ID: secure.NewID("aud"), ActorID: getPrincipal(r).User.ID, Action: action, TargetType: targetType, TargetID: targetID, Success: success, IP: clientIP(r), UserAgent: r.UserAgent(), Detail: raw, CreatedAt: time.Now().UTC()}
	if err := s.repo.AddAudit(r.Context(), event); err != nil {
		slog.Warn("감사 로그 저장 실패", "action", action, "error", err)
	}
}

func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusNotFound, "not_found", "요청한 경로를 찾을 수 없습니다")
		return
	}
	clean := filepath.Clean("/" + r.URL.Path)
	if strings.Contains(clean, "..") || strings.HasPrefix(filepath.Base(clean), ".") {
		writeError(w, http.StatusNotFound, "not_found", "요청한 경로를 찾을 수 없습니다")
		return
	}
	staticRoot := s.staticRoot
	if staticRoot == "" {
		staticRoot = defaultStaticRoot
	}
	candidate := filepath.Join(staticRoot, strings.TrimPrefix(clean, "/"))
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		setStaticCacheHeaders(w, clean)
		if contentType := mime.TypeByExtension(filepath.Ext(candidate)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		http.ServeFile(w, r, candidate)
		return
	}
	if strings.HasPrefix(clean, "/assets/") || isRootStaticAsset(clean) {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusNotFound, "asset_not_found", "요청한 정적 파일을 찾을 수 없습니다")
		return
	}
	index := filepath.Join(staticRoot, "index.html")
	if _, err := os.Stat(index); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "요청한 경로를 찾을 수 없습니다")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, index)
}

func setStaticCacheHeaders(w http.ResponseWriter, path string) {
	if strings.HasPrefix(path, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
}

func isRootStaticAsset(path string) bool {
	switch path {
	case "/favicon.ico", "/icon-192.png", "/icon-512.png", "/manifest.webmanifest", "/moina-logo.svg", "/moina-mark.svg":
		return true
	default:
		return false
	}
}
