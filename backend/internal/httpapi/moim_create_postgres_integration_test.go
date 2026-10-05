package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

// createMoim decided between 409 slug_taken and 500 storage_error inside one
// condition that also committed the transaction, so the INSERT error and the
// Commit error reached the same branch. Splitting them keeps the decision
// bound to the INSERT error, which is the only one store.IsConflict ever saw:
// a taken slug stays 409 and anything else stays 500. Both answers travel the
// real handler → pgx → server path, the conflict through PostgreSQL's own
// unique index rather than an injected error.
func TestPostgreSQLCreateMoimSeparatesSlugConflictFromStorageError(t *testing.T) {
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
	secrets, err := secure.New(bytes.Repeat([]byte{62}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	founderID := fmt.Sprintf("usr_create_moim_%d", suffix)
	takenSlug := fmt.Sprintf("create-taken-%d", suffix)
	// The trigger below makes PostgreSQL refuse exactly this slug, so the
	// refusal is a real *pgconn.PgError carrying a SQLSTATE that is not a
	// unique violation — which is what has to stay 500 after the split.
	poisonSlug := fmt.Sprintf("create-poison-%d", suffix)
	triggerFn := fmt.Sprintf("moina_test_refuse_moim_%d", suffix)
	triggerName := fmt.Sprintf("moina_test_refuse_moim_trg_%d", suffix)

	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON moims`, triggerName))
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, triggerFn))
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM moims WHERE owner_id=$1`, founderID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=$1`, founderID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=$1`, founderID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=$1`, founderID)
	})

	username := fmt.Sprintf("u%s", founderID[len("usr_"):])
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles) VALUES($1,$2,$2,ARRAY['member']::text[])`, founderID, username); err != nil {
		t.Fatal(err)
	}
	token := fmt.Sprintf("create-moim-token-%d", suffix)
	csrf := fmt.Sprintf("create-moim-csrf-%d", suffix)
	if err := repository.CreateSession(ctx, model.Session{
		ID: fmt.Sprintf("session_create_moim_%d", suffix), UserID: founderID,
		TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.slug = TG_ARGV[0] THEN
				RAISE EXCEPTION 'moina test: storage refused this moim';
			END IF;
			RETURN NEW;
		END $$`, triggerFn)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON moims FOR EACH ROW EXECUTE FUNCTION %s(%s)`, triggerName, triggerFn, pgQuoteLiteral(poisonSlug))); err != nil {
		t.Fatal(err)
	}

	// Handler() captures slog.Default() for the request-scoped logger, so this
	// is what lets the storage failure below be read back as the operator would
	// see it. Nothing in this package runs in parallel, and the writer is
	// guarded because the access log line is written from the same chain.
	logs := &lockedBuffer{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	server := New(repository, secrets, "v0.1.43-test")
	handler := server.Handler()
	type apiError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	create := func(slug string) (int, apiError, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"name": "테스트 Moim", "slug": slug})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/moims", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusCreated {
			var envelope struct {
				Data struct {
					Slug string `json:"slug"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("201 본문을 읽을 수 없습니다: %s", response.Body.String())
			}
			return response.Code, apiError{}, envelope.Data.Slug
		}
		var apiErr apiError
		if err := json.Unmarshal(response.Body.Bytes(), &apiErr); err != nil {
			t.Fatalf("응답 %d 본문을 읽을 수 없습니다: %s", response.Code, response.Body.String())
		}
		return response.Code, apiErr, ""
	}
	moimCount := func(slug string) int {
		t.Helper()
		var count int
		if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM moims WHERE slug=$1`, slug).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	t.Run("a free slug is created", func(t *testing.T) {
		code, apiErr, slug := create(takenSlug)
		if code != http.StatusCreated || slug != takenSlug {
			t.Fatalf("응답 = %d %+v slug=%q", code, apiErr, slug)
		}
		if got := moimCount(takenSlug); got != 1 {
			t.Fatalf("Moim 행 수 = %d", got)
		}
	})

	t.Run("a taken slug stays 409 slug_taken", func(t *testing.T) {
		code, apiErr, _ := create(takenSlug)
		if code != http.StatusConflict || apiErr.Code != "slug_taken" || apiErr.Message != "이미 사용 중인 Moim slug입니다" {
			t.Fatalf("응답 = %d %+v", code, apiErr)
		}
		if got := moimCount(takenSlug); got != 1 {
			t.Fatalf("충돌한 요청이 Moim을 더 만들었습니다: %d", got)
		}
	})

	t.Run("a refused INSERT is 500 storage_error", func(t *testing.T) {
		logs.reset()
		code, apiErr, _ := create(poisonSlug)
		if code != http.StatusInternalServerError || apiErr.Code != "storage_error" || apiErr.Message != "Moim을 만들 수 없습니다" {
			t.Fatalf("저장 오류가 감춰졌습니다: %d %+v", code, apiErr)
		}
		if got := moimCount(poisonSlug); got != 0 {
			t.Fatalf("실패한 생성이 남았습니다: %d", got)
		}
		// "Moim을 만들 수 없습니다" is written at more than one exit of this
		// handler, so a 500 with no cause in the log leaves the operator
		// unable to tell which statement was refused.
		written := logs.String()
		for _, field := range []string{
			`"error_code":"storage_error"`,
			`"handler":"createMoim"`,
			`"cause_type":"*pgconn.PgError"`,
			`"pg_code":"P0001"`,
		} {
			if !strings.Contains(written, field) {
				t.Errorf("운영자 로그에 %s가 없습니다: %s", field, written)
			}
		}
		if strings.Contains(written, "moina test: storage refused") {
			t.Errorf("pg 메시지 전문이 로그에 노출되었습니다: %s", written)
		}
	})
}
