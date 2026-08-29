package observability

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const RequestIDHeader = "X-Request-ID"

type contextKey uint8

const (
	requestIDKey contextKey = iota
	loggerKey
	flowSQLCounterKey
)

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

func Logger(ctx context.Context) *slog.Logger {
	logger, _ := ctx.Value(loggerKey).(*slog.Logger)
	if logger == nil {
		return slog.Default()
	}
	return logger
}

func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestID := strings.TrimSpace(request.Header.Get(RequestIDHeader))
		if !validRequestID(requestID) {
			requestID = newRequestID()
		}
		w.Header().Set(RequestIDHeader, requestID)
		ctx := context.WithValue(request.Context(), requestIDKey, requestID)
		next.ServeHTTP(w, request.WithContext(ctx))
	})
}

func AccessLogMiddleware(base *slog.Logger) func(http.Handler) http.Handler {
	if base == nil {
		base = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			started := time.Now()
			requestID := RequestID(request.Context())
			logger := base.With(
				"request_id", requestID,
				"http_method", request.Method,
				"http_path", request.URL.Path,
			)
			ctx := context.WithValue(request.Context(), loggerKey, logger)
			writer := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(writer, request.WithContext(ctx))
			logger.InfoContext(ctx, "HTTP 요청 완료",
				"http_status", writer.status,
				"response_bytes", writer.bytes,
				"duration_ms", time.Since(started).Milliseconds(),
			)
		})
	}
}

func HTTPMiddleware(base *slog.Logger) func(http.Handler) http.Handler {
	access := AccessLogMiddleware(base)
	return func(next http.Handler) http.Handler {
		return RequestIDMiddleware(access(next))
	}
}

func (registry *Registry) FlowMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		started := time.Now()
		counter := &atomic.Uint64{}
		ctx := context.WithValue(request.Context(), flowSQLCounterKey, counter)
		next.ServeHTTP(w, request.WithContext(ctx))
		registry.ObserveFlowLatency(time.Since(started))
		registry.ObserveFlowSQLQueries(counter.Load())
	})
}

func (registry *Registry) SearchMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, request)
		registry.ObserveSearchLatency(time.Since(started))
	})
}

type responseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (writer *responseWriter) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.wroteHeader = true
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *responseWriter) Write(body []byte) (int, error) {
	if !writer.wroteHeader {
		writer.wroteHeader = true
	}
	written, err := writer.ResponseWriter.Write(body)
	writer.bytes += int64(written)
	return written, err
}

func (writer *responseWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

func (writer *responseWriter) Flush() {
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := writer.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (writer *responseWriter) Push(target string, options *http.PushOptions) error {
	pusher, ok := writer.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, options)
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
}
