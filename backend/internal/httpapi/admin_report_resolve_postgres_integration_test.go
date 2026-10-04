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

// resolveReport folded two different outcomes into one answer. The UPDATE on
// reports reported `err != nil || RowsAffected() == 0` as 404 "신고를 찾을 수
// 없습니다", so a storage failure told the moderator the report they had just
// clicked in the list did not exist. And the moderation_actions INSERT that
// records the sanction ran after the UPDATE with its error discarded, so a
// failure there left the report marked resolved with no moderation record and
// the moderator saw "신고를 처리했습니다."
//
// The requests below go through the production wiring — New(repository…) and
// server.Handler(), a real sessions row, the CSRF header and pgx — and the
// storage failures are produced by test-only triggers that refuse one sentinel
// row each, not by a stand-in repository.
func TestPostgreSQLAdminResolveReportSeparatesStorageFailureFromMissingReport(t *testing.T) {
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
	adminID := fmt.Sprintf("usr_resolve_admin_%d", suffix)
	resolvedReportID := fmt.Sprintf("rep_resolve_ok_%d", suffix)
	reviewingReportID := fmt.Sprintf("rep_resolve_reviewing_%d", suffix)
	badStatusReportID := fmt.Sprintf("rep_resolve_badstatus_%d", suffix)
	updatePoisonReportID := fmt.Sprintf("rep_resolve_poison_update_%d", suffix)
	actionPoisonReportID := fmt.Sprintf("rep_resolve_poison_action_%d", suffix)
	missingReportID := fmt.Sprintf("rep_resolve_missing_%d", suffix)
	reportIDs := []string{resolvedReportID, reviewingReportID, badStatusReportID, updatePoisonReportID, actionPoisonReportID}
	token := fmt.Sprintf("resolve-token-%d", suffix)
	csrf := fmt.Sprintf("resolve-csrf-%d", suffix)
	updateTriggerFn := fmt.Sprintf("moina_test_resolve_update_%d", suffix)
	updateTriggerName := fmt.Sprintf("moina_test_resolve_update_trg_%d", suffix)
	actionTriggerFn := fmt.Sprintf("moina_test_resolve_action_%d", suffix)
	actionTriggerName := fmt.Sprintf("moina_test_resolve_action_trg_%d", suffix)

	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON reports`, updateTriggerName))
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, updateTriggerFn))
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON moderation_actions`, actionTriggerName))
		_, _ = repository.Pool().Exec(cleanupContext, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, actionTriggerFn))
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM moderation_actions WHERE report_id=ANY($1::text[])`, reportIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM reports WHERE id=ANY($1::text[])`, reportIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=$1`, adminID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=$1`, adminID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=$1`, adminID)
	})

	// The seeded admin role carries moderation:manage, which is what the
	// PATCH /admin/reports/{reportID} route requires.
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles) VALUES($1,$2,$2,ARRAY['admin']::text[])`, adminID, fmt.Sprintf("u%s", adminID[len("usr_"):])); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSession(ctx, model.Session{
		ID: fmt.Sprintf("session_resolve_%d", suffix), UserID: adminID,
		TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range reportIDs {
		if _, err := repository.Pool().Exec(ctx, `INSERT INTO reports(id,reporter_id,target_type,target_id,reason,status) VALUES($1,$2,'user',$2,'스팸','open')`, id, adminID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.id = TG_ARGV[0] THEN
				RAISE EXCEPTION 'moina test: storage refused this report update';
			END IF;
			RETURN NEW;
		END $$`, updateTriggerFn)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE ON reports FOR EACH ROW EXECUTE FUNCTION %s(%s)`, updateTriggerName, updateTriggerFn, pgQuoteLiteral(updatePoisonReportID))); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.report_id = TG_ARGV[0] THEN
				RAISE EXCEPTION 'moina test: storage refused this moderation action';
			END IF;
			RETURN NEW;
		END $$`, actionTriggerFn)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON moderation_actions FOR EACH ROW EXECUTE FUNCTION %s(%s)`, actionTriggerName, actionTriggerFn, pgQuoteLiteral(actionPoisonReportID))); err != nil {
		t.Fatal(err)
	}

	// Handler() captures slog.Default() for the request-scoped logger, so this
	// is what lets the storage failures below be read back as the operator
	// would see them. Nothing in this package runs in parallel, and the writer
	// is guarded because the access log line is written from the same chain.
	logs := &lockedBuffer{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	server := New(repository, secrets, "v0.1.38-test")
	handler := server.Handler()
	type apiError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	patch := func(t *testing.T, reportID, body string) (int, []byte) {
		t.Helper()
		logs.reset()
		request := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/reports/"+reportID, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code, response.Body.Bytes()
	}
	reportRow := func(t *testing.T, reportID string) (string, string, bool) {
		t.Helper()
		var status, resolution string
		var resolvedAt *time.Time
		if err := repository.Pool().QueryRow(ctx, `SELECT status,resolution,resolved_at FROM reports WHERE id=$1`, reportID).Scan(&status, &resolution, &resolvedAt); err != nil {
			t.Fatal(err)
		}
		return status, resolution, resolvedAt != nil
	}
	actionCount := func(t *testing.T, reportID string) int {
		t.Helper()
		var count int
		if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FROM moderation_actions WHERE report_id=$1`, reportID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	cases := []struct {
		name           string
		reportID       string
		body           string
		wantStatus     int
		wantCode       string
		wantMessage    string
		wantRowStatus  string
		wantResolution string
		wantResolvedAt bool
		wantActions    int
		// The SQLSTATE the operator log must carry for a refused write. The
		// test triggers below use a bare RAISE EXCEPTION, which is P0001.
		wantPgCode string
	}{
		{
			name: "resolving a report records the moderation action", reportID: resolvedReportID,
			body: `{"status":"resolved","resolution":"제재 완료"}`, wantStatus: http.StatusOK,
			wantRowStatus: "resolved", wantResolution: "제재 완료", wantResolvedAt: true, wantActions: 1,
		},
		{
			name: "marking a report reviewing records no moderation action", reportID: reviewingReportID,
			body: `{"status":"reviewing","resolution":""}`, wantStatus: http.StatusOK,
			wantRowStatus: "reviewing", wantResolution: "", wantResolvedAt: false, wantActions: 0,
		},
		{
			name: "an unsupported status is rejected before storage", reportID: badStatusReportID,
			body: `{"status":"archived","resolution":"제재 완료"}`, wantStatus: http.StatusBadRequest,
			wantCode: "invalid_resolution", wantRowStatus: "open", wantResolution: "", wantActions: 0,
		},
		{
			// The report id is absent, which is the only case the 404 message
			// is true about.
			name: "a report that does not exist is not found", reportID: missingReportID,
			body: `{"status":"resolved","resolution":"제재 완료"}`, wantStatus: http.StatusNotFound,
			wantCode: "not_found", wantMessage: "신고를 찾을 수 없습니다",
		},
		{
			name: "a failed report update is a storage error, not a missing report", reportID: updatePoisonReportID,
			body: `{"status":"resolved","resolution":"제재 완료"}`, wantStatus: http.StatusInternalServerError,
			wantCode: "storage_error", wantRowStatus: "open", wantResolution: "", wantActions: 0,
			wantPgCode: "P0001",
		},
		{
			// Without the moderation action there is no record of who
			// sanctioned what, so the resolution must not stand on its own.
			name: "a failed moderation action leaves the report unresolved", reportID: actionPoisonReportID,
			body: `{"status":"resolved","resolution":"제재 완료"}`, wantStatus: http.StatusInternalServerError,
			wantCode: "storage_error", wantRowStatus: "open", wantResolution: "", wantActions: 0,
			wantPgCode: "P0001",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, payload := patch(t, testCase.reportID, testCase.body)
			if status != testCase.wantStatus {
				t.Fatalf("status=%d 기대=%d 본문=%s", status, testCase.wantStatus, string(payload))
			}
			if testCase.wantCode != "" {
				var failure apiError
				if err := json.Unmarshal(payload, &failure); err != nil {
					t.Fatalf("오류 본문을 읽을 수 없습니다: %s", string(payload))
				}
				if failure.Code != testCase.wantCode {
					t.Fatalf("code=%q 기대=%q 본문=%s", failure.Code, testCase.wantCode, string(payload))
				}
				if testCase.wantMessage != "" && failure.Message != testCase.wantMessage {
					t.Fatalf("message=%q 기대=%q", failure.Message, testCase.wantMessage)
				}
			}
			// A 500 with no cause in the log leaves the operator with the
			// user's complaint as the only clue, so the refused write has to
			// name itself and its SQLSTATE next to the request id.
			if testCase.wantPgCode != "" {
				written := logs.String()
				for _, field := range []string{
					`"error_code":"storage_error"`,
					`"handler":"resolveReport"`,
					`"cause_type":"*pgconn.PgError"`,
					fmt.Sprintf(`"pg_code":%q`, testCase.wantPgCode),
				} {
					if !strings.Contains(written, field) {
						t.Errorf("운영자 로그에 %s가 없습니다: %s", field, written)
					}
				}
				if !strings.Contains(written, `"request_id":"`) || strings.Contains(written, `"request_id":""`) {
					t.Errorf("운영자 로그에 request_id가 없습니다: %s", written)
				}
				if strings.Contains(written, "moina test: storage refused") {
					t.Errorf("pg 메시지 전문이 로그에 노출되었습니다: %s", written)
				}
			}
			if testCase.reportID == missingReportID {
				return
			}
			rowStatus, rowResolution, resolvedAt := reportRow(t, testCase.reportID)
			if rowStatus != testCase.wantRowStatus || rowResolution != testCase.wantResolution {
				t.Fatalf("reports status=%q resolution=%q 기대 status=%q resolution=%q", rowStatus, rowResolution, testCase.wantRowStatus, testCase.wantResolution)
			}
			if resolvedAt != testCase.wantResolvedAt {
				t.Fatalf("reports.resolved_at 존재=%t 기대=%t", resolvedAt, testCase.wantResolvedAt)
			}
			if got := actionCount(t, testCase.reportID); got != testCase.wantActions {
				t.Fatalf("moderation_actions 행=%d 기대=%d", got, testCase.wantActions)
			}
		})
	}
}
