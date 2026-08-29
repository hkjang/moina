package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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

func TestWorkflowDisabledSkipsApproval(t *testing.T) {
	if workflowMatches(model.WorkflowConfig{Enabled: false, Actions: []string{"*"}}, "post.publish") {
		t.Fatal("비활성 승인 정책이 작업을 가로챘습니다")
	}
	if !workflowMatches(model.WorkflowConfig{Enabled: true, Actions: []string{"post.*"}}, "post.publish") {
		t.Fatal("승인 wildcard가 일치하지 않습니다")
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
	if !slices.Equal(updated.Feed.ExcludedTopics, []string{"go", "데이터"}) || updated.Notifications.Desktop {
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
