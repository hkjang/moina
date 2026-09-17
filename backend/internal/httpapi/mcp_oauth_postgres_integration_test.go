package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/store"
)

// The account half of MCP SSO: a Keycloak token opens tools/list for an
// account the web sign-in already linked, with the scopes the administrator
// chose and no more; it never creates an account, never revives an inactive
// one, and never opens REST. Keys keep working next to it.
func TestPostgreSQLMCPOAuthOpensMCPForALinkedAccountOnly(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	ctx := t.Context()
	repository, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)

	suffix := time.Now().UnixNano()
	memberID := fmt.Sprintf("usr_mcpsso_member_%d", suffix)
	inactiveID := fmt.Sprintf("usr_mcpsso_inactive_%d", suffix)
	memberSubject := fmt.Sprintf("mcpsso-member-%d", suffix)
	inactiveSubject := fmt.Sprintf("mcpsso-inactive-%d", suffix)
	strangerSubject := fmt.Sprintf("mcpsso-stranger-%d", suffix)
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM api_keys WHERE user_id=ANY($1::text[])`, []string{memberID, inactiveID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{memberID, inactiveID})
	})

	idp := newFakeIDP(t)
	general := defaultGeneral()
	general.PublicBaseURL = "https://moina.example"
	cfg := mcpOAuthConfig(idp, true)
	cfg.MCPOAuth.Audience = []string{"claude-mcp"}
	server := mcpOAuthTestServer(t, idp, cfg, &general)
	server.repo = repository
	server.outbox = nil
	handler := server.Handler()

	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,email,provider,roles,active) VALUES
		($1,$2,$2,'member@example.com','oidc',ARRAY['member']::text[],true),
		($3,$4,$4,'inactive@example.com','oidc',ARRAY['member']::text[],false)`,
		memberID, "mcpsso_member_"+fmt.Sprint(suffix), inactiveID, "mcpsso_inactive_"+fmt.Sprint(suffix)); err != nil {
		t.Fatal(err)
	}
	// The identity link is what a web sign-in leaves behind; nothing here
	// creates one.
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO oidc_identities(issuer,subject,user_id) VALUES ($1,$2,$3),($1,$4,$5)`, idp.server.URL, memberSubject, memberID, inactiveSubject, inactiveID); err != nil {
		t.Fatal(err)
	}
	resource := "https://moina.example/mcp"
	toolNames := func(recorder *httptest.ResponseRecorder) []string {
		t.Helper()
		var response struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
			Error *mcpError `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode %s: %v", recorder.Body.String(), err)
		}
		if response.Error != nil {
			t.Fatalf("tools/list error: %+v", response.Error)
		}
		names := make([]string, 0, len(response.Result.Tools))
		for _, tool := range response.Result.Tools {
			names = append(names, tool.Name)
		}
		return names
	}

	// A token for this resource opens MCP for the linked account, with the
	// administrator's scopes (posts:read mcp:use) intersected with the
	// member role: read tools only, no posts.create, no ai.status.
	opened := mcpCall(handler, "/mcp", idp.accessToken(t, resource, map[string]any{"sub": memberSubject}))
	if opened.Code != http.StatusOK {
		t.Fatalf("token for this resource refused: %d %s", opened.Code, opened.Body.String())
	}
	names := strings.Join(toolNames(opened), " ")
	if !strings.Contains(names, "moina.flow.read") || strings.Contains(names, "moina.posts.create") || strings.Contains(names, "moina.ai.status") {
		t.Fatalf("tools visible to the SSO subject = %s, want read tools only", names)
	}
	// Measured against a real Keycloak 26: aud is ["account"] and the client
	// is in azp, so the administrator's audience list must carry it through
	// without any mapper, and the web client's own id is trusted as well.
	for _, azp := range []string{"claude-mcp", cfg.ClientID} {
		viaAZP := mcpCall(handler, "/api/v1/mcp", idp.accessToken(t, "account", map[string]any{"sub": memberSubject, "azp": azp}))
		if viaAZP.Code != http.StatusOK {
			t.Fatalf("azp %q refused: %d %s", azp, viaAZP.Code, viaAZP.Body.String())
		}
	}
	// A token carrying MOINA's own vocabulary narrows the grant further.
	narrowed := mcpCall(handler, "/mcp", idp.accessToken(t, resource, map[string]any{"sub": memberSubject, "scope": "openid mcp:use"}))
	if narrowed.Code != http.StatusOK || len(toolNames(narrowed)) != 0 {
		t.Fatalf("scope-narrowed token: %d %s", narrowed.Code, narrowed.Body.String())
	}

	// No account is created for a stranger and none is revived for an
	// inactive one; both are told to sign in on the web first.
	for name, subject := range map[string]string{"stranger": strangerSubject, "inactive": inactiveSubject} {
		refused := mcpCall(handler, "/mcp", idp.accessToken(t, resource, map[string]any{"sub": subject}))
		if refused.Code != http.StatusUnauthorized || !strings.Contains(refused.Body.String(), "먼저 웹으로 한 번 로그인") {
			t.Fatalf("%s: %d %s", name, refused.Code, refused.Body.String())
		}
	}
	var strangers int
	if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM oidc_identities WHERE subject=$1`, strangerSubject).Scan(&strangers); err != nil || strangers != 0 {
		t.Fatalf("stranger provisioned: %d %v", strangers, err)
	}

	// The same token on REST is not a credential at all.
	request := httptest.NewRequest(http.MethodGet, "https://moina.example/api/v1/auth/me", nil)
	request.Header.Set("Authorization", "Bearer "+idp.accessToken(t, resource, map[string]any{"sub": memberSubject}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("REST with a valid SSO token: %d %q", recorder.Code, recorder.Header().Get("WWW-Authenticate"))
	}

	// And the key door is untouched: a personal key opens MCP exactly as
	// before, with its own scopes.
	token, err := newAPIKeyToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateAPIKey(ctx, model.APIKey{ID: fmt.Sprintf("key_mcpsso_%d", suffix), UserID: memberID, Name: "mcp", Prefix: token[:12], TokenHash: server.secrets.HashToken(token), Permissions: []string{"posts:read", "posts:write", "mcp:use"}}, memberID); err != nil {
		t.Fatal(err)
	}
	viaKey := mcpCall(handler, "/mcp", token)
	if viaKey.Code != http.StatusOK || !strings.Contains(strings.Join(toolNames(viaKey), " "), "moina.posts.create") {
		t.Fatalf("key: %d %s", viaKey.Code, viaKey.Body.String())
	}
}
