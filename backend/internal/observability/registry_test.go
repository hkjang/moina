package observability

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type poolStatsStub struct{}

func (poolStatsStub) MaxConns() int32                { return 25 }
func (poolStatsStub) TotalConns() int32              { return 7 }
func (poolStatsStub) AcquiredConns() int32           { return 3 }
func (poolStatsStub) IdleConns() int32               { return 4 }
func (poolStatsStub) ConstructingConns() int32       { return 0 }
func (poolStatsStub) AcquireCount() int64            { return 12 }
func (poolStatsStub) CanceledAcquireCount() int64    { return 2 }
func (poolStatsStub) AcquireDuration() time.Duration { return 350 * time.Millisecond }

func TestRegistryPrometheusText(t *testing.T) {
	registry := NewRegistry()
	registry.ObserveFlowLatency(75 * time.Millisecond)
	registry.IncSQLQueries()
	registry.IncOutboxFailures()
	registry.SetOutboxLag(3 * time.Second)
	registry.IncWebSocketConnections()
	registry.ObserveDBPool(poolStatsStub{})

	response := httptest.NewRecorder()
	registry.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	for _, fragment := range []string{
		"moina_flow_latency_seconds_count 1",
		"moina_sql_queries_total 1",
		"moina_outbox_lag_seconds 3",
		"moina_outbox_failures_total 1",
		`moina_db_pool_connections{state="acquired"} 3`,
		"moina_websocket_connections 1",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("metrics missing %q\n%s", fragment, body)
		}
	}
}

func TestPGXTracerCountsWithoutRetainingSQL(t *testing.T) {
	registry := NewRegistry()
	tracer := PGXTracer{Registry: registry}
	tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT secret", Args: []any{"secret"}})
	response := httptest.NewRecorder()
	registry.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), "moina_sql_queries_total 1") {
		t.Fatal("query was not counted")
	}
	if strings.Contains(response.Body.String(), "secret") {
		t.Fatal("metrics leaked SQL or arguments")
	}
}

func TestFlowMiddlewareCountsQueriesInRequestContext(t *testing.T) {
	registry := NewRegistry()
	tracer := PGXTracer{Registry: registry}
	handler := registry.FlowMiddleware(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		for range 4 {
			tracer.TraceQueryStart(request.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
		}
		for _, sql := range []string{"BEGIN", "COMMIT", "ROLLBACK", "SELECT pg_advisory_xact_lock(1)", "SELECT pg_try_advisory_xact_lock(1)"} {
			tracer.TraceQueryStart(request.Context(), nil, pgx.TraceQueryStartData{SQL: sql})
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/feed", nil))
	metrics := httptest.NewRecorder()
	registry.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metrics.Body.String()
	for _, fragment := range []string{
		`moina_flow_sql_queries_bucket{le="5"} 1`,
		"moina_flow_sql_queries_sum 4",
		"moina_flow_sql_queries_count 1",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("metrics missing %q\n%s", fragment, body)
		}
	}
}

func TestSearchMiddlewareRecordsLatencyHistogram(t *testing.T) {
	registry := NewRegistry()
	handler := registry.SearchMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/search?q=go", nil))
	metrics := httptest.NewRecorder()
	registry.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metrics.Body.String()
	if !strings.Contains(body, "moina_search_latency_seconds_count 1") {
		t.Fatalf("search latency was not recorded:\n%s", body)
	}
}

func TestHTTPMiddlewareRequestIDAndStructuredLog(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := HTTPMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if RequestID(request.Context()) != "trusted-id" {
			t.Errorf("request ID = %q", RequestID(request.Context()))
		}
		if Logger(request.Context()) == nil {
			t.Error("request logger missing")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/posts", nil)
	request.Header.Set(RequestIDHeader, "trusted-id")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get(RequestIDHeader) != "trusted-id" {
		t.Fatalf("response request ID = %q", response.Header().Get(RequestIDHeader))
	}
	for _, fragment := range []string{`"request_id":"trusted-id"`, `"http_status":201`, `"response_bytes":2`} {
		if !strings.Contains(logs.String(), fragment) {
			t.Errorf("log missing %q: %s", fragment, logs.String())
		}
	}
}

func TestRequestIDMiddlewareRejectsLogInjection(t *testing.T) {
	handler := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = w.Write([]byte(RequestID(request.Context())))
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(RequestIDHeader, "bad\nid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Body.String() == "bad\nid" || !validRequestID(response.Body.String()) {
		t.Fatalf("unsafe request ID accepted: %q", response.Body.String())
	}
}
