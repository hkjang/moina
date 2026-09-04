package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moina/backend/internal/model"
)

func TestRecommendationReasonsMatchRankingScore(t *testing.T) {
	prefs := feedPreferences{TopicWeight: 30, LinkWeight: 20, DiscoveryWeight: 10, RecencyWeight: 40, ShowReasons: true}
	post := model.Moin{
		Author:    model.User{Following: true},
		Topics:    []model.Topic{{Following: true}, {Following: true}},
		Signals:   map[string]int64{"like": 2, "insight": 3},
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}
	reasons, score := recommendationComponents(post, prefs)
	sum := 0.0
	for _, reason := range reasons {
		sum += reason.Score
	}
	if difference := sum - score; difference > 0.000001 || difference < -0.000001 {
		t.Fatalf("설명 점수 합 %.6f와 랭킹 점수 %.6f가 다릅니다", sum, score)
	}
	secondScore := recommendationScore(post, prefs)
	if difference := score - secondScore; difference > 0.001 || difference < -0.001 {
		t.Fatal("랭킹 함수가 설명 컴포넌트와 다른 공식을 사용합니다")
	}
}

func TestBrowserSessionOrPermissionAllowsProfileMediaWithoutPostRole(t *testing.T) {
	server := &Server{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := server.requireBrowserSessionOrPermission("posts:write")(next)
	tests := []struct {
		name       string
		principal  principal
		wantStatus int
	}{
		{name: "browser session", principal: principal{}, wantStatus: http.StatusNoContent},
		{name: "scoped API key", principal: principal{APIKey: true, Permissions: []string{"posts:write"}}, wantStatus: http.StatusNoContent},
		{name: "unscoped API key", principal: principal{APIKey: true, Permissions: []string{"posts:read"}}, wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/media", nil)
			request = request.WithContext(withPrincipal(request, test.principal))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRecommendationDiscoveryAndRecencyAreExplicit(t *testing.T) {
	post := model.Moin{Signals: map[string]int64{}, CreatedAt: time.Now()}
	reasons, _ := recommendationComponents(post, defaultFeedPreferences())
	labels := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		labels = append(labels, reason.Label)
	}
	if len(labels) != 2 || !slices.Contains(labels, "새로운 관심사·Signal 발견") || !slices.Contains(labels, "최근 24시간의 새 Moin") {
		t.Fatalf("설명 가능한 추천 컴포넌트가 빠졌습니다: %v", labels)
	}
}

func TestExtractHashtagsAndMentionsDeduplicates(t *testing.T) {
	if got := extractHashtags("#Go #go #데이터베이스"); !slices.Equal(got, []string{"go", "데이터베이스"}) {
		t.Fatalf("hashtags=%v", got)
	}
	if got := extractMentions("@Alice 안녕하세요 @alice @홍길동"); !slices.Equal(got, []string{"alice", "홍길동"}) {
		t.Fatalf("mentions=%v", got)
	}
	if got := extractMentions("mail@example.com 붙여쓴@alice 정상 @bob_user"); !slices.Equal(got, []string{"bob_user"}) {
		t.Fatalf("email/embedded text must not become mentions: %v", got)
	}
}

func TestValidateAIEnforces256KCeiling(t *testing.T) {
	valid := model.AIConfig{Enabled: true, BaseURL: "https://ai.internal/v1", Model: "local-model", APIStyle: "responses", DefaultMaxTokens: 4096, MaxTokens: 262144, TimeoutSeconds: 300}
	if err := validateAI(&valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.MaxTokens++
	if err := validateAI(&invalid); err == nil {
		t.Fatal("262144보다 큰 서비스 토큰 상한을 허용했습니다")
	}
	invalid = valid
	invalid.DefaultMaxTokens = invalid.MaxTokens + 1
	if err := validateAI(&invalid); err == nil {
		t.Fatal("기본 토큰이 서비스 상한을 넘었습니다")
	}
}

func TestOutboundSettingsRequireExactAllowedHost(t *testing.T) {
	ai := model.AIConfig{Enabled: true, BaseURL: "https://ai.internal/v1", AllowedHosts: []string{"other.internal"}, Model: "local-model", APIStyle: "responses", DefaultMaxTokens: 4096, MaxTokens: 262144, TimeoutSeconds: 300}
	if err := validateAI(&ai); err == nil {
		t.Fatal("허용 목록 밖 AI 호스트를 허용했습니다")
	}
	oidc := defaultOIDC()
	oidc.Enabled = true
	oidc.IssuerURL = "https://keycloak.internal/realms/moina"
	oidc.ClientID = "moina"
	oidc.AllowedHosts = []string{"other.internal"}
	normalizeOIDC(&oidc)
	if err := validateOIDC(oidc, true); err == nil {
		t.Fatal("허용 목록 밖 OIDC 호스트를 허용했습니다")
	}
}

func TestWorkflowDisabledSkipsApproval(t *testing.T) {
	if workflowMatches(model.WorkflowConfig{Enabled: false, Actions: []string{"*"}}, "post.publish") {
		t.Fatal("비활성 승인 정책이 작업을 가로챘습니다")
	}
	if !workflowMatches(model.WorkflowConfig{Enabled: true, Actions: []string{"post.*"}}, "post.publish") {
		t.Fatal("승인 wildcard가 일치하지 않습니다")
	}
}

func TestApprovalActionPatternGrammarAndMatching(t *testing.T) {
	valid := []struct {
		pattern string
		action  string
		match   bool
	}{
		{"*", "post.publish", true},
		{"POST.PUBLISH", "post.publish", true},
		{"post.publish", "post.publish.external", false},
		{"post.*", "post.publish", true},
		{"post.*", "postal.publish", false},
		{"post.moderation.*", "post.moderation.publish", true},
	}
	for _, item := range valid {
		parsed, err := parseApprovalActionPattern(item.pattern)
		if err != nil {
			t.Errorf("유효 패턴 %q 거부: %v", item.pattern, err)
			continue
		}
		if got := parsed.matches(item.action); got != item.match {
			t.Errorf("패턴 %q action %q match=%t, want %t", item.pattern, item.action, got, item.match)
		}
	}

	invalid := []string{
		"", "post", ".post", "post.", "post..publish", "post*", "post.publ*",
		"*post", "post.*.publish", "post.*.*", "post.**", "post..*", "*.*",
		"post:*", "post:**", "approvals:review", "post/publish",
	}
	for _, pattern := range invalid {
		if _, err := parseApprovalActionPattern(pattern); err == nil {
			t.Errorf("잘못된 승인 action 패턴을 허용했습니다: %q", pattern)
		}
		if workflowMatches(model.WorkflowConfig{Enabled: true, Actions: []string{pattern}}, "post.publish") {
			t.Errorf("잘못된 저장 패턴이 action과 일치했습니다: %q", pattern)
		}
	}
}

func TestApprovalActionPatternsMapToImplementedProducer(t *testing.T) {
	for _, raw := range []string{"*", "post.*", "post.publish", "POST.PUBLISH"} {
		pattern, err := parseApprovalActionPattern(raw)
		if err != nil || !implementedApprovalPattern(pattern) {
			t.Errorf("implemented action pattern %q is not usable: %v", raw, err)
		}
	}
	for _, raw := range []string{"moim.member.approve", "agent.post.publish", "moim.*"} {
		pattern, err := parseApprovalActionPattern(raw)
		if err != nil {
			t.Fatalf("syntactically valid future action %q failed parsing: %v", raw, err)
		}
		if implementedApprovalPattern(pattern) {
			t.Errorf("unsupported action pattern %q unexpectedly maps to post.publish", raw)
		}
	}
}

func TestVerifyOriginChecksBearerAndSafeMethods(t *testing.T) {
	server := &Server{}
	handler := server.verifyOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodGet, "https://moina.internal/mcp", nil)
	request.Host = "moina.internal"
	request.Header.Set("Origin", "https://evil.example")
	request.Header.Set("Authorization", "Bearer mk_example")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin Bearer GET status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "https://moina.internal/mcp", nil)
	request.Host = "moina.internal"
	request.Header.Set("Origin", "http://moina.internal")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-scheme same-host status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "https://moina.internal/mcp", nil)
	request.Host = "moina.internal"
	request.Header.Set("Origin", "https://moina.internal")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("same-origin status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "https://moina.internal/mcp", nil)
	request.Host = "moina.internal"
	request.Header.Set("Origin", "https://moina.internal:443")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("same-origin HTTPS default port status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "https://moina.internal/mcp", nil)
	request.Host = "moina.internal"
	request.Header.Set("Origin", "https://moina.internal:80")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("wrong HTTPS default port status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "http://moina.internal/mcp", nil)
	request.Host = "moina.internal"
	request.Header.Set("Origin", "http://moina.internal:80")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("same-origin HTTP default port status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "http://moina.internal/mcp", nil)
	request.Host = "moina.internal"
	request.Header.Set("Origin", "http://moina.internal:443")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("wrong HTTP default port status=%d", recorder.Code)
	}
}

func TestVerifyOriginAllowsStaticModuleBehindTLSProxy(t *testing.T) {
	server := &Server{}
	handler := server.verifyOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodGet, "http://moina.internal/assets/index-example.js", nil)
	request.Host = "moina.internal"
	request.Header.Set("Origin", "https://moina.internal")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("TLS proxy module asset status=%d", recorder.Code)
	}
}

func TestVerifyOriginUsesSameOriginFetchMetadataForProxyBootstrap(t *testing.T) {
	server := &Server{}
	handler := server.verifyOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isHTTPS(r) {
			t.Error("browser-visible HTTPS was not detected")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "http://moina:8080/api/v1/auth/login", nil)
	request.Host = "moina:8080"
	request.Header.Set("Origin", "https://social.example")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("TLS proxy bootstrap status=%d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "http://moina:8080/api/v1/auth/login", nil)
	request.Host = "moina:8080"
	request.Header.Set("Origin", "https://social.example")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-site proxy bootstrap status=%d", recorder.Code)
	}
}

func TestServeSPAAssetCachingAndFallback(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><title>MOINA</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "index-hash.js"), []byte("export const ready = true;"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := &Server{staticRoot: root}

	recorder := httptest.NewRecorder()
	server.serveSPA(recorder, httptest.NewRequest(http.MethodGet, "http://moina.internal/assets/index-hash.js", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("existing asset status=%d content-type=%q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("existing asset cache-control=%q", got)
	}

	recorder = httptest.NewRecorder()
	server.serveSPA(recorder, httptest.NewRequest(http.MethodGet, "http://moina.internal/assets/stale-hash.js", nil))
	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "<title>MOINA</title>") {
		t.Fatalf("missing asset status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("missing asset cache-control=%q", got)
	}

	recorder = httptest.NewRecorder()
	server.serveSPA(recorder, httptest.NewRequest(http.MethodGet, "http://moina.internal/flow", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "<title>MOINA</title>") {
		t.Fatalf("SPA route status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("SPA route cache-control=%q", got)
	}
}

func TestPreferencesPartialUpdateIsStrictAndPreservesOtherSections(t *testing.T) {
	patch, err := decodePreferencesPatch(json.RawMessage(`{"appearance":{"fontScale":125},"feed":{"topicWeight":0,"excludedTopics":["#Go","go","데이터"]},"notifications":{"desktop":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := applyPreferencesPatch(defaultPreferencesDocument(), patch)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Appearance.FontScale != 125 || updated.Appearance.Theme != "system" || updated.Feed.TopicWeight != 0 || updated.Feed.LinkWeight != 30 {
		t.Fatalf("부분 업데이트가 기존 설정을 보존하지 않습니다: %+v", updated)
	}
	if !slices.Equal(updated.Feed.ExcludedTopics, []string{"go", "데이터"}) || updated.Notifications.Desktop.Enabled {
		t.Fatalf("제외 토픽/알림 정규화가 올바르지 않습니다: %+v", updated)
	}
}

func TestPreferencesRejectInvalidTypesRangesNullsAndUnknownFields(t *testing.T) {
	invalid := []string{
		`{"appearance":{"theme":"neon"}}`,
		`{"appearance":{"fontScale":1000}}`,
		`{"appearance":{"density":"dense"}}`,
		`{"feed":{"topicWeight":-1}}`,
		`{"feed":{"recencyWeight":101}}`,
		`{"feed":{"excludedTopics":["two words"]}}`,
		`{"notifications":{"desktop":"yes"}}`,
		`{"notifications":{"desktop":null}}`,
		`{"appearance":{"unknown":true}}`,
	}
	for _, raw := range invalid {
		patch, err := decodePreferencesPatch(json.RawMessage(raw))
		if err == nil {
			_, err = applyPreferencesPatch(defaultPreferencesDocument(), patch)
		}
		if err == nil {
			t.Errorf("잘못된 개인화 설정을 허용했습니다: %s", raw)
		}
	}
}

func TestMCPPermissionFiltersTools(t *testing.T) {
	tools := visibleMCPTools(principal{Permissions: []string{"posts:read", "mcp:use"}})
	for _, tool := range tools {
		if tool.Name == "moina.posts.create" || tool.Name == "moina.echo.create" || tool.Name == "moina.ai.status" {
			t.Fatalf("권한 없는 도구가 노출됐습니다: %s", tool.Name)
		}
	}
	if mcpProtocolVersion != "2025-11-25" {
		t.Fatalf("unexpected MCP protocol version %s", mcpProtocolVersion)
	}
}

func mcpToolSchema(t *testing.T, name string) map[string]any {
	t.Helper()
	for _, tool := range mcpTools() {
		if tool.Name == name {
			return tool.InputSchema
		}
	}
	t.Fatalf("도구를 찾을 수 없습니다: %s", name)
	return nil
}

// A dropped argument used to be invisible to the caller: the tool ran with the
// default and returned a plausible answer to a different question.
func TestMCPRejectsArgumentsOutsidePublishedSchema(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		tool      string
		arguments map[string]any
	}{
		{name: "상한을 넘는 limit", tool: "moina.flow.read", arguments: map[string]any{"limit": float64(500)}},
		{name: "0인 limit", tool: "moina.notifications.list", arguments: map[string]any{"limit": float64(0)}},
		{name: "문자열 limit", tool: "moina.flow.read", arguments: map[string]any{"limit": "50"}},
		{name: "정수가 아닌 limit", tool: "moina.flow.read", arguments: map[string]any{"limit": 20.5}},
		{name: "이름이 틀린 인자", tool: "moina.flow.read", arguments: map[string]any{"count": float64(20)}},
		{name: "enum 밖 mode", tool: "moina.flow.read", arguments: map[string]any{"mode": "trending"}},
		{name: "enum 밖 visibility", tool: "moina.posts.create", arguments: map[string]any{"content": "안녕하세요", "visibility": "secret"}},
		{name: "문자열이 아닌 id", tool: "moina.posts.get", arguments: map[string]any{"id": float64(7)}},
		{name: "빠뜨린 필수 인자", tool: "moina.echo.create", arguments: map[string]any{"content": "좋은 글이네요"}},
		{name: "인자를 받지 않는 도구", tool: "moina.ai.status", arguments: map[string]any{"limit": float64(10)}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			failure := validateMCPArguments(mcpToolSchema(t, testCase.tool), testCase.arguments)
			if failure == nil {
				t.Fatalf("스키마를 벗어난 arguments를 허용했습니다: %v", testCase.arguments)
			}
			if failure.Code != -32602 || failure.Message == "" {
				t.Fatalf("failure=%+v", failure)
			}
		})
	}
}

func TestMCPAcceptsArgumentsMatchingPublishedSchema(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		tool      string
		arguments map[string]any
	}{
		{name: "선택 인자 생략", tool: "moina.flow.read", arguments: map[string]any{}},
		{name: "경계값 limit", tool: "moina.flow.read", arguments: map[string]any{"mode": "following", "limit": float64(100)}},
		{name: "공백이 붙은 enum", tool: "moina.flow.read", arguments: map[string]any{"mode": " following "}},
		{name: "필수 인자", tool: "moina.echo.create", arguments: map[string]any{"postId": "p1", "content": "좋은 글이네요"}},
		{name: "arguments 없음", tool: "moina.ai.status", arguments: nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if failure := validateMCPArguments(mcpToolSchema(t, testCase.tool), testCase.arguments); failure != nil {
				t.Fatalf("올바른 arguments를 거절했습니다: %+v", failure)
			}
		})
	}
}

func TestMCPDoesNotExposeApprovalDecisionTool(t *testing.T) {
	for _, tool := range mcpTools() {
		if tool.Permission == "approvals:review" {
			t.Fatalf("MCP가 브라우저 세션 전용 승인 결정을 노출했습니다: %s", tool.Name)
		}
	}
}

func TestDetectMediaTypeByMagic(t *testing.T) {
	mp4 := append([]byte{0, 0, 0, 24}, []byte("ftypisom0000")...)
	if got := detectMediaType(mp4); got != "video/mp4" {
		t.Fatalf("mp4=%q", got)
	}
	webm := append([]byte{0x1a, 0x45, 0xdf, 0xa3}, []byte("webm")...)
	if got := detectMediaType(webm); got != "video/webm" {
		t.Fatalf("webm=%q", got)
	}
}

func TestNotificationAliasesMatchUI(t *testing.T) {
	server := &Server{}
	item := model.Notification{Type: "reaction", Payload: json.RawMessage(`{}`)}
	server.decorateNotification(t.Context(), &item)
	if item.Type != "signal" || item.Title == "" {
		t.Fatalf("notification=%+v", item)
	}
	mention := model.Notification{Type: "mention", TargetID: "moin-target", Payload: json.RawMessage(`{"postId":"moin-payload"}`)}
	server.decorateNotification(t.Context(), &mention)
	if mention.Type != "mention" || mention.Title != "새로운 멘션" || mention.TargetPath != "/moin/moin-payload" {
		t.Fatalf("mention notification=%+v", mention)
	}
}

func TestListTopicsRejectsUnknownSort(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/topics?sort=oldest", nil)
	recorder := httptest.NewRecorder()
	new(Server).listTopics(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"invalid_sort"`) {
		t.Fatalf("unknown Topic sort status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestSafeReturnToRejectsExternalURL(t *testing.T) {
	if safeReturnTo("https://evil.example") || safeReturnTo("//evil.example") || !safeReturnTo("/feed?mode=for_me") {
		t.Fatal("OIDC returnTo 검증이 올바르지 않습니다")
	}
}

func TestCanonicalRoutesAreRegistered(t *testing.T) {
	handler := New(nil, nil, "v0.0.0-test").Handler()
	routes := map[string]bool{}
	if err := chi.Walk(handler.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	required := []string{
		"POST /api/v1/auth/login", "GET /api/v1/feed", "POST /api/v1/posts",
		"DELETE /api/v1/posts/{postID}/remoin", "GET /api/v1/ws/notifications",
		"POST /api/v1/moims/{slug}/members", "POST /api/v1/ai/chat", "POST /api/v1/mcp",
		"PATCH /api/v1/admin/reports/{reportID}", "PUT /api/v1/admin/oidc", "PUT /api/v1/admin/workflow",
	}
	for _, route := range required {
		if !routes[route] {
			t.Errorf("canonical route missing: %s", route)
		}
	}
}
