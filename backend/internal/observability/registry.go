// Package observability provides dependency-light runtime metrics and HTTP
// middleware. The registry emits Prometheus text format without requiring a
// networked collector or an additional runtime service.
package observability

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

var defaultFlowBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type Registry struct {
	flowMu         sync.RWMutex
	flowBuckets    []float64
	flowCounts     []uint64
	flowCount      uint64
	flowSum        float64
	flowSQLBuckets []float64
	flowSQLCounts  []uint64
	flowSQLCount   uint64
	flowSQLSum     uint64
	searchBuckets  []float64
	searchCounts   []uint64
	searchCount    uint64
	searchSum      float64

	sqlQueries     atomic.Uint64
	outboxFailures atomic.Uint64
	outboxLagBits  atomic.Uint64
	websocketConns atomic.Int64
	dbMax          atomic.Int64
	dbTotal        atomic.Int64
	dbAcquired     atomic.Int64
	dbIdle         atomic.Int64
	dbConstructing atomic.Int64
	dbAcquires     atomic.Int64
	dbCanceled     atomic.Int64
	dbAcquireNanos atomic.Int64
}

func NewRegistry() *Registry {
	buckets := append([]float64(nil), defaultFlowBuckets...)
	sqlBuckets := []float64{0, 1, 2, 3, 5, 10, 20, 50, 100}
	return &Registry{
		flowBuckets: buckets, flowCounts: make([]uint64, len(buckets)),
		flowSQLBuckets: sqlBuckets, flowSQLCounts: make([]uint64, len(sqlBuckets)),
		searchBuckets: append([]float64(nil), buckets...), searchCounts: make([]uint64, len(buckets)),
	}
}

func (registry *Registry) ObserveFlowLatency(duration time.Duration) {
	if registry == nil {
		return
	}
	seconds := duration.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	registry.flowMu.Lock()
	defer registry.flowMu.Unlock()
	registry.flowCount++
	registry.flowSum += seconds
	for index, upper := range registry.flowBuckets {
		if seconds <= upper {
			registry.flowCounts[index]++
		}
	}
}

func (registry *Registry) ObserveFlowSQLQueries(count uint64) {
	if registry == nil {
		return
	}
	registry.flowMu.Lock()
	defer registry.flowMu.Unlock()
	registry.flowSQLCount++
	registry.flowSQLSum += count
	for index, upper := range registry.flowSQLBuckets {
		if float64(count) <= upper {
			registry.flowSQLCounts[index]++
		}
	}
}

func (registry *Registry) ObserveSearchLatency(duration time.Duration) {
	if registry == nil {
		return
	}
	seconds := duration.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	registry.flowMu.Lock()
	defer registry.flowMu.Unlock()
	registry.searchCount++
	registry.searchSum += seconds
	for index, upper := range registry.searchBuckets {
		if seconds <= upper {
			registry.searchCounts[index]++
		}
	}
}

func (registry *Registry) IncSQLQueries() {
	if registry != nil {
		registry.sqlQueries.Add(1)
	}
}

func (registry *Registry) IncOutboxFailures() {
	if registry != nil {
		registry.outboxFailures.Add(1)
	}
}

func (registry *Registry) SetOutboxLag(lag time.Duration) {
	if registry == nil {
		return
	}
	seconds := lag.Seconds()
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		seconds = 0
	}
	registry.outboxLagBits.Store(math.Float64bits(seconds))
}

func (registry *Registry) IncWebSocketConnections() {
	if registry != nil {
		registry.websocketConns.Add(1)
	}
}

func (registry *Registry) DecWebSocketConnections() {
	if registry == nil {
		return
	}
	for {
		current := registry.websocketConns.Load()
		if current == 0 || registry.websocketConns.CompareAndSwap(current, current-1) {
			return
		}
	}
}

type DBPoolStats interface {
	MaxConns() int32
	TotalConns() int32
	AcquiredConns() int32
	IdleConns() int32
	ConstructingConns() int32
	AcquireCount() int64
	CanceledAcquireCount() int64
	AcquireDuration() time.Duration
}

func (registry *Registry) ObserveDBPool(stats DBPoolStats) {
	if registry == nil || stats == nil {
		return
	}
	registry.dbMax.Store(int64(stats.MaxConns()))
	registry.dbTotal.Store(int64(stats.TotalConns()))
	registry.dbAcquired.Store(int64(stats.AcquiredConns()))
	registry.dbIdle.Store(int64(stats.IdleConns()))
	registry.dbConstructing.Store(int64(stats.ConstructingConns()))
	registry.dbAcquires.Store(stats.AcquireCount())
	registry.dbCanceled.Store(stats.CanceledAcquireCount())
	registry.dbAcquireNanos.Store(int64(stats.AcquireDuration()))
}

func (registry *Registry) Handler() http.Handler { return registry }

func (registry *Registry) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	registry.flowMu.RLock()
	buckets := append([]float64(nil), registry.flowBuckets...)
	counts := append([]uint64(nil), registry.flowCounts...)
	flowCount, flowSum := registry.flowCount, registry.flowSum
	flowSQLBuckets := append([]float64(nil), registry.flowSQLBuckets...)
	flowSQLCounts := append([]uint64(nil), registry.flowSQLCounts...)
	flowSQLCount, flowSQLSum := registry.flowSQLCount, registry.flowSQLSum
	searchBuckets := append([]float64(nil), registry.searchBuckets...)
	searchCounts := append([]uint64(nil), registry.searchCounts...)
	searchCount, searchSum := registry.searchCount, registry.searchSum
	registry.flowMu.RUnlock()

	fprintf(w, "# HELP moina_flow_latency_seconds Flow 피드 요청 처리 시간.\n")
	fprintf(w, "# TYPE moina_flow_latency_seconds histogram\n")
	for index, upper := range buckets {
		fprintf(w, "moina_flow_latency_seconds_bucket{le=%q} %d\n", formatFloat(upper), counts[index])
	}
	fprintf(w, "moina_flow_latency_seconds_bucket{le=\"+Inf\"} %d\n", flowCount)
	fprintf(w, "moina_flow_latency_seconds_sum %s\n", formatFloat(flowSum))
	fprintf(w, "moina_flow_latency_seconds_count %d\n", flowCount)
	fprintf(w, "# HELP moina_flow_sql_queries Flow 피드 요청당 PostgreSQL query/exec 수.\n")
	fprintf(w, "# TYPE moina_flow_sql_queries histogram\n")
	for index, upper := range flowSQLBuckets {
		fprintf(w, "moina_flow_sql_queries_bucket{le=%q} %d\n", formatFloat(upper), flowSQLCounts[index])
	}
	fprintf(w, "moina_flow_sql_queries_bucket{le=\"+Inf\"} %d\n", flowSQLCount)
	fprintf(w, "moina_flow_sql_queries_sum %d\n", flowSQLSum)
	fprintf(w, "moina_flow_sql_queries_count %d\n", flowSQLCount)
	fprintf(w, "# HELP moina_search_latency_seconds 통합 검색 요청 처리 시간.\n")
	fprintf(w, "# TYPE moina_search_latency_seconds histogram\n")
	for index, upper := range searchBuckets {
		fprintf(w, "moina_search_latency_seconds_bucket{le=%q} %d\n", formatFloat(upper), searchCounts[index])
	}
	fprintf(w, "moina_search_latency_seconds_bucket{le=\"+Inf\"} %d\n", searchCount)
	fprintf(w, "moina_search_latency_seconds_sum %s\n", formatFloat(searchSum))
	fprintf(w, "moina_search_latency_seconds_count %d\n", searchCount)

	writeCounter(w, "moina_sql_queries_total", "실행된 PostgreSQL query/exec 수.", registry.sqlQueries.Load())
	fprintf(w, "# HELP moina_outbox_lag_seconds 가장 오래 대기 중인 outbox 이벤트 지연.\n")
	fprintf(w, "# TYPE moina_outbox_lag_seconds gauge\n")
	fprintf(w, "moina_outbox_lag_seconds %s\n", formatFloat(math.Float64frombits(registry.outboxLagBits.Load())))
	writeCounter(w, "moina_outbox_failures_total", "Outbox handler 실패 수.", registry.outboxFailures.Load())

	fprintf(w, "# HELP moina_db_pool_connections PostgreSQL pool 연결 수.\n")
	fprintf(w, "# TYPE moina_db_pool_connections gauge\n")
	for _, item := range []struct {
		state string
		value int64
	}{
		{"max", registry.dbMax.Load()}, {"total", registry.dbTotal.Load()},
		{"acquired", registry.dbAcquired.Load()}, {"idle", registry.dbIdle.Load()},
		{"constructing", registry.dbConstructing.Load()},
	} {
		fprintf(w, "moina_db_pool_connections{state=%q} %d\n", item.state, item.value)
	}
	writeCounter(w, "moina_db_pool_acquires_total", "PostgreSQL pool acquire 성공 수.", uint64(max(registry.dbAcquires.Load(), 0)))
	writeCounter(w, "moina_db_pool_canceled_acquires_total", "취소된 PostgreSQL pool acquire 수.", uint64(max(registry.dbCanceled.Load(), 0)))
	fprintf(w, "# HELP moina_db_pool_acquire_duration_seconds_total PostgreSQL pool acquire 누적 대기 시간.\n")
	fprintf(w, "# TYPE moina_db_pool_acquire_duration_seconds_total counter\n")
	fprintf(w, "moina_db_pool_acquire_duration_seconds_total %s\n", formatFloat(time.Duration(max(registry.dbAcquireNanos.Load(), 0)).Seconds()))

	fprintf(w, "# HELP moina_websocket_connections 현재 WebSocket 연결 수.\n")
	fprintf(w, "# TYPE moina_websocket_connections gauge\n")
	fprintf(w, "moina_websocket_connections %d\n", max(registry.websocketConns.Load(), 0))
}

func writeCounter(w http.ResponseWriter, name, help string, value uint64) {
	fprintf(w, "# HELP %s %s\n", name, help)
	fprintf(w, "# TYPE %s counter\n", name)
	fprintf(w, "%s %d\n", name, value)
}

func fprintf(w http.ResponseWriter, format string, values ...any) {
	_, _ = fmt.Fprintf(w, format, values...)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// PGXTracer can be assigned to pgx.ConnConfig.Tracer before the pool is opened.
// It counts operations only and deliberately does not retain SQL or arguments.
type PGXTracer struct{ Registry *Registry }

func (tracer PGXTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tracer.Registry.IncSQLQueries()
	if counter, ok := ctx.Value(flowSQLCounterKey).(*atomic.Uint64); ok && counter != nil {
		normalized := strings.ToUpper(strings.TrimSpace(data.SQL))
		if normalized != "BEGIN" && normalized != "COMMIT" && normalized != "ROLLBACK" &&
			!strings.Contains(normalized, "PG_ADVISORY_XACT_LOCK") &&
			!strings.Contains(normalized, "PG_TRY_ADVISORY_XACT_LOCK") {
			counter.Add(1)
		}
	}
	return ctx
}

func (PGXTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

var _ pgx.QueryTracer = PGXTracer{}
