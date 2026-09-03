package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hkjang/moina/backend/internal/model"
)

func TestPaginationAcceptsDocumentedRange(t *testing.T) {
	for _, testCase := range []struct {
		query  string
		limit  int
		offset int
	}{
		{query: "", limit: 30, offset: 0},
		{query: "?limit=1&offset=0", limit: 1, offset: 0},
		{query: "?limit=100&offset=1000000", limit: 100, offset: 1_000_000},
		{query: "?offset=", limit: 30, offset: 0},
		// A list endpoint issues its next offset as nextCursor, so the client
		// hands the number back as cursor.
		{query: "?cursor=60", limit: 30, offset: 60},
		// A Flow cursor is opaque and decoded by the endpoint itself.
		{query: "?cursor=eyJ2IjoxfQ", limit: 30, offset: 0},
		// An explicit offset wins over a cursor.
		{query: "?offset=10&cursor=60", limit: 30, offset: 10},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://moina.internal/api/v1/posts"+testCase.query, nil)
		limit, offset, ok := pagination(recorder, request)
		if !ok || limit != testCase.limit || offset != testCase.offset {
			t.Fatalf("%q: limit=%d offset=%d ok=%t, %d·%d를 기대했습니다", testCase.query, limit, offset, ok, testCase.limit, testCase.offset)
		}
	}
}

func TestPaginationRejectsValuesOutsideTheContract(t *testing.T) {
	for _, query := range []string{
		"?limit=abc",
		"?limit=0",
		"?limit=-1",
		"?limit=101",
		"?limit=1e2",
		"?offset=abc",
		"?offset=-1",
		"?offset=1000001",
		// Following nextCursor past the ceiling used to silently restart at
		// page 1, so a client looping on the cursor never terminated.
		"?cursor=1000030",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://moina.internal/api/v1/posts"+query, nil)
		if _, _, ok := pagination(recorder, request); ok {
			t.Fatalf("%q가 허용되었습니다", query)
		}
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%q 응답 코드가 %d입니다", query, recorder.Code)
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Code != "invalid_pagination" {
			t.Fatalf("%q 응답 code가 %q입니다: %v", query, body.Code, err)
		}
	}
}

func TestListEnvelopeStopsAtTheOffsetCeiling(t *testing.T) {
	items := make([]model.Moin, 30)
	if cursor, ok := listEnvelope(items, 30, 999_970)["nextCursor"]; !ok || cursor != "1000000" {
		t.Fatalf("상한에 닿는 page의 nextCursor가 %v입니다", cursor)
	}
	if cursor, ok := listEnvelope(items, 30, 999_971)["nextCursor"]; ok {
		t.Fatalf("상한을 넘는 nextCursor %v를 발급했습니다", cursor)
	}
}

func TestPaginationRejectionKeepsHandlersFromRunning(t *testing.T) {
	// A repository-less server would panic on any query, so reaching the 200
	// path at all proves the guard is missing.
	server := New(nil, nil, "test")
	for name, handler := range map[string]http.HandlerFunc{
		"listPosts":         server.listPosts,
		"listTopics":        server.listTopics,
		"listMoims":         server.listMoims,
		"listNotifications": server.listNotifications,
		"adminListUsers":    server.adminListUsers,
		"adminListOutbox":   server.adminListOutbox,
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://moina.internal/api/v1/posts?limit=500", nil)
		handler(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s 응답 코드가 %d입니다: %s", name, recorder.Code, recorder.Body.String())
		}
	}
}
