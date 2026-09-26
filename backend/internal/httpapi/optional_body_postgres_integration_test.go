package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

// Handlers whose JSON body is optional used to gate the decode on
// r.ContentLength > 0. Go reports ContentLength == -1 for a chunked request (and
// for HTTP/2 without a content-length), so that gate threw away a body the client
// had actually sent: following a Topic stored the default weight 50 while
// answering 200 with the weight the caller asked for being silently discarded,
// and the reject paths answered 400 for a reason they had been given.
//
// The requests below travel a real httptest.NewServer over the wire, so the
// transport — not a hand-set field — decides the framing: an io.NopCloser hides
// the length from net/http and it sends Transfer-Encoding: chunked. Every chunked
// case is paired with a content-length twin carrying the same bytes, because the
// contract is that both framings read the same input as the same value.
func TestPostgreSQLOptionalJSONBodySurvivesChunkedRequests(t *testing.T) {
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
	secrets, err := secure.New(bytes.Repeat([]byte{61}, 32))
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	adminID := fmt.Sprintf("usr_optbody_admin_%d", suffix)
	topicID := fmt.Sprintf("top_optbody_%d", suffix)
	topicSlug := fmt.Sprintf("optbody-topic-%d", suffix)
	chunkedReportID := fmt.Sprintf("rep_optbody_chunked_%d", suffix)
	lengthReportID := fmt.Sprintf("rep_optbody_length_%d", suffix)
	emptyReportID := fmt.Sprintf("rep_optbody_empty_%d", suffix)
	reportIDs := []string{chunkedReportID, lengthReportID, emptyReportID}
	missingApprovalID := fmt.Sprintf("apr_optbody_missing_%d", suffix)
	token := fmt.Sprintf("optbody-token-%d", suffix)
	csrf := fmt.Sprintf("optbody-csrf-%d", suffix)

	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM moderation_actions WHERE report_id=ANY($1::text[])`, reportIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM reports WHERE id=ANY($1::text[])`, reportIDs)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM user_topic_follows WHERE user_id=$1`, adminID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM topics WHERE id=$1`, topicID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM outbox_events WHERE aggregate_id=$1`, adminID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM sessions WHERE user_id=$1`, adminID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM audit_events WHERE actor_id=$1`, adminID)
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=$1`, adminID)
	})

	// The seeded admin role carries social:write, admin:access, moderation:manage and
	// approvals:review, and defaultWorkflow() lists admin as an approver role, so one
	// user reaches all three handlers that share the optional-body contract.
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name,roles) VALUES($1,$2,$2,ARRAY['admin']::text[])`, adminID, fmt.Sprintf("u%s", adminID[len("usr_"):])); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSession(ctx, model.Session{
		ID: fmt.Sprintf("session_optbody_%d", suffix), UserID: adminID,
		TokenHash: secrets.HashToken(token), CSRFHash: secrets.HashToken(csrf),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO topics(id,slug,name) VALUES($1,$2,'선택 본문 Topic')`, topicID, topicSlug); err != nil {
		t.Fatal(err)
	}
	for _, id := range reportIDs {
		if _, err := repository.Pool().Exec(ctx, `INSERT INTO reports(id,reporter_id,target_type,target_id,reason,status) VALUES($1,$2,'user',$2,'스팸','open')`, id, adminID); err != nil {
			t.Fatal(err)
		}
	}

	server := New(repository, secrets, "v0.1.37-test")
	// observedContentLength records what the production handler chain was actually
	// handed, so the chunked cases prove r.ContentLength == -1 rather than assuming it.
	var observedContentLength atomic.Int64
	handler := server.Handler()
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedContentLength.Store(r.ContentLength)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(testServer.Close)

	// chunked hides the body length from net/http, which then frames the request with
	// Transfer-Encoding: chunked. knownLength keeps *strings.Reader so http.NewRequest
	// fills in Content-Length.
	chunked := func(body string) io.Reader { return io.NopCloser(strings.NewReader(body)) }
	knownLength := func(body string) io.Reader { return strings.NewReader(body) }

	type apiError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	send := func(t *testing.T, path string, body io.Reader) (int, []byte, int64) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, testServer.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := testServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, payload, observedContentLength.Load()
	}
	decodeError := func(t *testing.T, payload []byte) apiError {
		t.Helper()
		var failure apiError
		if err := json.Unmarshal(payload, &failure); err != nil {
			t.Fatalf("오류 본문을 읽을 수 없습니다: %s", string(payload))
		}
		return failure
	}

	followPath := "/api/v1/topics/" + topicSlug + "/follow"
	// storedWeight answers -1 when the caller is not following the Topic at all, so a
	// rejected request can be told apart from one that stored the default.
	storedWeight := func(t *testing.T) int {
		t.Helper()
		var weight int
		err := repository.Pool().QueryRow(ctx, `SELECT weight FROM user_topic_follows WHERE user_id=$1 AND topic_id=$2`, adminID, topicID).Scan(&weight)
		if store.IsNotFound(err) {
			return -1
		}
		if err != nil {
			t.Fatal(err)
		}
		return weight
	}

	t.Run("follow", func(t *testing.T) {
		cases := []struct {
			name              string
			body              func() io.Reader
			wantStatus        int
			wantCode          string
			wantMessage       string
			wantWeight        int
			wantContentLength int64
		}{
			{
				name: "chunked body stores the weight the client sent", body: func() io.Reader { return chunked(`{"weight":80}`) },
				wantStatus: http.StatusOK, wantWeight: 80, wantContentLength: -1,
			},
			{
				name: "content-length body stores the weight the client sent", body: func() io.Reader { return knownLength(`{"weight":80}`) },
				wantStatus: http.StatusOK, wantWeight: 80, wantContentLength: 13,
			},
			{
				name: "absent body keeps the default weight", body: func() io.Reader { return nil },
				wantStatus: http.StatusOK, wantWeight: 50, wantContentLength: 0,
			},
			{
				name: "empty content-length body keeps the default weight", body: func() io.Reader { return knownLength("") },
				wantStatus: http.StatusOK, wantWeight: 50, wantContentLength: 0,
			},
			{
				name: "whitespace-only chunked body keeps the default weight", body: func() io.Reader { return chunked("   ") },
				wantStatus: http.StatusOK, wantWeight: 50, wantContentLength: -1,
			},
			{
				// A known length says the client meant to send something, so an empty
				// decode stays the client's mistake. This is the case that would break
				// if the EOF tolerance were not restricted to ContentLength < 0.
				name: "whitespace-only content-length body stays invalid JSON", body: func() io.Reader { return knownLength("   ") },
				wantStatus: http.StatusBadRequest, wantCode: "invalid_json", wantWeight: -1, wantContentLength: 3,
			},
			{
				name: "malformed content-length body stays invalid JSON", body: func() io.Reader { return knownLength("{") },
				wantStatus: http.StatusBadRequest, wantCode: "invalid_json", wantWeight: -1, wantContentLength: 1,
			},
			{
				name: "malformed chunked body is invalid JSON too", body: func() io.Reader { return chunked("{") },
				wantStatus: http.StatusBadRequest, wantCode: "invalid_json", wantWeight: -1, wantContentLength: -1,
			},
			{
				name: "unknown field stays invalid JSON", body: func() io.Reader { return knownLength(`{"weight":80,"bogus":1}`) },
				wantStatus: http.StatusBadRequest, wantCode: "invalid_json", wantWeight: -1, wantContentLength: 23,
			},
			{
				name: "two JSON values stay rejected", body: func() io.Reader { return knownLength(`{"weight":80}{"weight":90}`) },
				wantStatus: http.StatusBadRequest, wantCode: "invalid_json", wantMessage: "JSON 값은 하나만 허용됩니다", wantWeight: -1, wantContentLength: 26,
			},
			{
				name: "body over the limit stays rejected", body: func() io.Reader {
					return knownLength(`{"weight":80,"pad":"` + strings.Repeat("x", maxBodyBytes) + `"}`)
				},
				wantStatus: http.StatusBadRequest, wantCode: "invalid_json", wantWeight: -1, wantContentLength: int64(maxBodyBytes + 22),
			},
			{
				name: "out-of-range weight in a chunked body is rejected", body: func() io.Reader { return chunked(`{"weight":101}`) },
				wantStatus: http.StatusBadRequest, wantCode: "invalid_weight", wantWeight: -1, wantContentLength: -1,
			},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				if _, err := repository.Pool().Exec(ctx, `DELETE FROM user_topic_follows WHERE user_id=$1`, adminID); err != nil {
					t.Fatal(err)
				}
				status, payload, contentLength := send(t, followPath, testCase.body())
				if contentLength != testCase.wantContentLength {
					t.Fatalf("핸들러가 본 ContentLength=%d, 기대=%d — 전송 프레이밍이 예상과 다릅니다", contentLength, testCase.wantContentLength)
				}
				if status != testCase.wantStatus {
					t.Fatalf("status=%d 기대=%d 본문=%s", status, testCase.wantStatus, string(payload))
				}
				if testCase.wantStatus == http.StatusOK {
					var envelope struct {
						Data struct {
							Following bool `json:"following"`
							Weight    int  `json:"weight"`
						} `json:"data"`
					}
					if err := json.Unmarshal(payload, &envelope); err != nil {
						t.Fatalf("200 본문을 읽을 수 없습니다: %s", string(payload))
					}
					if !envelope.Data.Following || envelope.Data.Weight != testCase.wantWeight {
						t.Fatalf("응답 following=%t weight=%d 기대 weight=%d", envelope.Data.Following, envelope.Data.Weight, testCase.wantWeight)
					}
				} else {
					failure := decodeError(t, payload)
					if failure.Code != testCase.wantCode {
						t.Fatalf("code=%q 기대=%q 본문=%s", failure.Code, testCase.wantCode, string(payload))
					}
					if testCase.wantMessage != "" && failure.Message != testCase.wantMessage {
						t.Fatalf("message=%q 기대=%q", failure.Message, testCase.wantMessage)
					}
				}
				if got := storedWeight(t); got != testCase.wantWeight {
					t.Fatalf("user_topic_follows.weight=%d 기대=%d", got, testCase.wantWeight)
				}
			})
		}
	})

	t.Run("report reject", func(t *testing.T) {
		cases := []struct {
			name           string
			reportID       string
			body           func() io.Reader
			wantStatus     int
			wantCode       string
			wantStatusRow  string
			wantResolution string
		}{
			{
				name: "chunked body supplies the resolution", reportID: chunkedReportID,
				body:       func() io.Reader { return chunked(`{"resolution":"chunked 처리 메모"}`) },
				wantStatus: http.StatusOK, wantStatusRow: "dismissed", wantResolution: "chunked 처리 메모",
			},
			{
				name: "content-length body supplies the resolution", reportID: lengthReportID,
				body:       func() io.Reader { return knownLength(`{"resolution":"length 처리 메모"}`) },
				wantStatus: http.StatusOK, wantStatusRow: "dismissed", wantResolution: "length 처리 메모",
			},
			{
				name: "absent body is still rejected", reportID: emptyReportID,
				body:       func() io.Reader { return nil },
				wantStatus: http.StatusBadRequest, wantCode: "invalid_resolution", wantStatusRow: "open", wantResolution: "",
			},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				status, payload, _ := send(t, "/api/v1/admin/reports/"+testCase.reportID+"/reject", testCase.body())
				if status != testCase.wantStatus {
					t.Fatalf("status=%d 기대=%d 본문=%s", status, testCase.wantStatus, string(payload))
				}
				if testCase.wantCode != "" {
					if failure := decodeError(t, payload); failure.Code != testCase.wantCode {
						t.Fatalf("code=%q 기대=%q 본문=%s", failure.Code, testCase.wantCode, string(payload))
					}
				}
				var rowStatus, rowResolution string
				if err := repository.Pool().QueryRow(ctx, `SELECT status,resolution FROM reports WHERE id=$1`, testCase.reportID).Scan(&rowStatus, &rowResolution); err != nil {
					t.Fatal(err)
				}
				if rowStatus != testCase.wantStatusRow || rowResolution != testCase.wantResolution {
					t.Fatalf("reports status=%q resolution=%q 기대 status=%q resolution=%q", rowStatus, rowResolution, testCase.wantStatusRow, testCase.wantResolution)
				}
			})
		}
	})

	// reviewApproval decodes the optional body before it looks the approval up, so a
	// comment that survives the decode turns the 400 comment_required into the 404 the
	// missing approval deserves. That ordering is what makes a missing row a usable
	// probe for whether the body was read at all.
	t.Run("approval reject", func(t *testing.T) {
		cases := []struct {
			name       string
			body       func() io.Reader
			wantStatus int
			wantCode   string
		}{
			{
				name: "chunked body supplies the comment", body: func() io.Reader { return chunked(`{"comment":"chunked 반려 사유"}`) },
				wantStatus: http.StatusNotFound, wantCode: "not_found",
			},
			{
				name: "content-length body supplies the comment", body: func() io.Reader { return knownLength(`{"comment":"length 반려 사유"}`) },
				wantStatus: http.StatusNotFound, wantCode: "not_found",
			},
			{
				name: "absent body is still rejected", body: func() io.Reader { return nil },
				wantStatus: http.StatusBadRequest, wantCode: "comment_required",
			},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				status, payload, _ := send(t, "/api/v1/approvals/"+missingApprovalID+"/reject", testCase.body())
				if status != testCase.wantStatus {
					t.Fatalf("status=%d 기대=%d 본문=%s", status, testCase.wantStatus, string(payload))
				}
				if failure := decodeError(t, payload); failure.Code != testCase.wantCode {
					t.Fatalf("code=%q 기대=%q 본문=%s", failure.Code, testCase.wantCode, string(payload))
				}
			})
		}
	})
}
