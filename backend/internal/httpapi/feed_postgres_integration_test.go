package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/observability"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLFlowSQLQueryBudget(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	ctx := t.Context()
	metrics := observability.NewRegistry()
	repository, err := store.OpenWithTracer(ctx, dsn, observability.PGXTracer{Registry: metrics})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	secrets, err := secure.New(bytes.Repeat([]byte{17}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	viewerID := fmt.Sprintf("usr_flow_budget_viewer_%d", suffix)
	authorID := fmt.Sprintf("usr_flow_budget_author_%d", suffix)
	postPrefix := fmt.Sprintf("post_flow_budget_%d_", suffix)
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles)
		VALUES($1,$1,$1,ARRAY['member']::text[]),($2,$2,$2,ARRAY['member']::text[])`, viewerID, authorID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repository.Pool().Exec(t.Context(), `DELETE FROM posts WHERE author_id=$1`, authorID)
		_, _ = repository.Pool().Exec(t.Context(), `DELETE FROM users WHERE id=ANY($1::text[])`, []string{viewerID, authorID})
	})
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO follows(follower_id,followee_id) VALUES($1,$2)`, viewerID, authorID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO posts(id,author_id,content,kind,visibility,status,published_at,created_at)
		SELECT $2||lpad(value::text,3,'0'),$1,'Flow SQL budget '||value,'moin','public','published',now()-make_interval(secs=>value),now()-make_interval(secs=>value)
		FROM generate_series(1,25) value`, authorID, postPrefix); err != nil {
		t.Fatal(err)
	}

	token := fmt.Sprintf("flow-budget-token-%d", suffix)
	if err := repository.CreateSession(ctx, model.Session{
		ID: "session_flow_budget_" + strconv.FormatInt(suffix, 10), UserID: viewerID,
		TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken("csrf"),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	server := New(repository, secrets, "v0.1.11")
	server.SetObservability(metrics)
	handler := server.Handler()

	requestFeed := func(path string) feedTestEnvelope {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, response.Code, response.Body.String())
		}
		var envelope feedTestEnvelope
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}

	before := flowSQLSum(t, metrics)
	first := requestFeed("/api/v1/feed?mode=for_me&limit=20")
	afterFirst := flowSQLSum(t, metrics)
	if delta := afterFirst - before; delta > 5 {
		t.Fatalf("For Me first page used %d application SQL statements, want <= 5", delta)
	}
	if len(first.Data.Items) != 20 || first.Data.NextCursor == "" {
		t.Fatalf("unexpected first page: items=%d cursor=%q", len(first.Data.Items), first.Data.NextCursor)
	}
	second := requestFeed("/api/v1/feed?mode=for_me&limit=20&cursor=" + url.QueryEscape(first.Data.NextCursor))
	afterSecond := flowSQLSum(t, metrics)
	if delta := afterSecond - afterFirst; delta > 5 {
		t.Fatalf("For Me next page used %d application SQL statements, want <= 5", delta)
	}
	if len(second.Data.Items) == 0 {
		t.Fatal("expected a non-empty second page")
	}

	_ = requestFeed("/api/v1/feed?mode=following&limit=20")
	afterFollowing := flowSQLSum(t, metrics)
	if delta := afterFollowing - afterSecond; delta > 3 {
		t.Fatalf("Following page used %d application SQL statements, want <= 3", delta)
	}
}

type feedTestEnvelope struct {
	Data struct {
		Items      []json.RawMessage `json:"items"`
		NextCursor string            `json:"nextCursor"`
	} `json:"data"`
}

func flowSQLSum(t *testing.T, registry *observability.Registry) uint64 {
	t.Helper()
	response := httptest.NewRecorder()
	registry.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(response.Body.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "moina_flow_sql_queries_sum" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	t.Fatal("moina_flow_sql_queries_sum metric is missing")
	return 0
}
