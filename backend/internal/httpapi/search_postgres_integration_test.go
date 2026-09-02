package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/store"
)

type searchResponse struct {
	Data struct {
		Query  string           `json:"query"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
		Users  []map[string]any `json:"users"`
		Posts  []model.Moin     `json:"posts"`
		Topics []model.Topic    `json:"topics"`
		Moims  []model.Moim     `json:"moims"`
	} `json:"data"`
}

func runSearch(t *testing.T, server *Server, viewer model.User, rawQuery string) searchResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/search?"+rawQuery, nil)
	request = request.WithContext(withPrincipal(request, principal{User: viewer, Permissions: []string{"posts:read"}}))
	response := httptest.NewRecorder()
	server.search(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var decoded searchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("응답 해석 실패: %v (%s)", err, response.Body.String())
	}
	return decoded
}

func TestPostgreSQLSearchHonoursOffset(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	repository, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)

	suffix := time.Now().UnixNano()
	term := fmt.Sprintf("offsetterm%d", suffix)
	viewerID := fmt.Sprintf("usr_offset_viewer_%d", suffix)
	authorID := fmt.Sprintf("usr_offset_author_%d", suffix)
	postIDs := []string{}
	for index := 0; index < 3; index++ {
		postIDs = append(postIDs, fmt.Sprintf("post_offset_%d_%d", suffix, index))
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES
		($1,$1,'Offset Viewer',ARRAY['member']::text[]),
		($2,$2,'Offset Author',ARRAY['member']::text[])`, viewerID, authorID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM posts WHERE id=ANY($1::text[])`, postIDs)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{viewerID, authorID})
	})
	for index, postID := range postIDs {
		// Distinct publication times give the result a stable order to page.
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO posts
			(id,author_id,content,kind,visibility,status,published_at,created_at)
			VALUES($1,$2,$3,'moin','public','published',statement_timestamp()-make_interval(mins=>$4::integer),statement_timestamp()-make_interval(mins=>$4::integer))`,
			postID, authorID, fmt.Sprintf("%s 본문 %d", term, index), index); err != nil {
			t.Fatal(err)
		}
	}

	server := New(repository, nil, "test")
	viewer := model.User{ID: viewerID}
	base := fmt.Sprintf("q=%s&type=posts&limit=2", term)

	first := runSearch(t, server, viewer, base)
	if len(first.Data.Posts) != 2 {
		t.Fatalf("첫 페이지 %d건, 2건을 기대했습니다", len(first.Data.Posts))
	}
	second := runSearch(t, server, viewer, base+"&offset=2")
	if len(second.Data.Posts) != 1 {
		t.Fatalf("두 번째 페이지 %d건, 1건을 기대했습니다", len(second.Data.Posts))
	}
	if second.Data.Offset != 2 {
		t.Fatalf("응답 offset=%d, 2를 기대했습니다", second.Data.Offset)
	}
	seen := map[string]bool{}
	for _, post := range append(append([]model.Moin{}, first.Data.Posts...), second.Data.Posts...) {
		if seen[post.ID] {
			t.Fatalf("offset 페이지가 %s를 중복 반환했습니다", post.ID)
		}
		seen[post.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("두 페이지에서 %d건을 봤습니다. 3건을 기대했습니다", len(seen))
	}
	// Without offset support the second page repeated the first one, so this is
	// the regression the documented parameter used to have.
	if second.Data.Posts[0].ID == first.Data.Posts[0].ID {
		t.Fatal("offset이 무시되어 첫 페이지가 반복되었습니다")
	}
}

func TestPostgreSQLBrowseModeRanksByPopularityWithoutQueryText(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	repository, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)

	suffix := time.Now().UnixNano()
	viewerID := fmt.Sprintf("usr_browse_viewer_%d", suffix)
	popularID := fmt.Sprintf("usr_browse_popular_%d", suffix)
	quietID := fmt.Sprintf("usr_browse_quiet_%d", suffix)
	followers := []string{}
	for index := 0; index < 3; index++ {
		followers = append(followers, fmt.Sprintf("usr_browse_follower_%d_%d", suffix, index))
	}
	everyone := append([]string{viewerID, popularID, quietID}, followers...)
	for _, id := range everyone {
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$1,$1,ARRAY['member']::text[])`, id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM follows WHERE follower_id=ANY($1::text[]) OR followee_id=ANY($1::text[])`, everyone)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=ANY($1::text[])`, everyone)
	})
	for _, follower := range followers {
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO follows(follower_id,followee_id) VALUES($1,$2)`, follower, popularID); err != nil {
			t.Fatal(err)
		}
	}

	server := New(repository, nil, "test")
	result := runSearch(t, server, model.User{ID: viewerID}, "recommended=true&type=users&limit=100")
	if result.Data.Query != "" {
		t.Fatalf("browse 모드 query=%q, 빈 문자열을 기대했습니다", result.Data.Query)
	}
	popularRank, quietRank := -1, -1
	for index, user := range result.Data.Users {
		switch user["id"] {
		case popularID:
			popularRank = index
		case quietID:
			quietRank = index
		case viewerID:
			t.Fatal("추천 목록에 본인이 포함되었습니다")
		}
	}
	if popularRank < 0 || quietRank < 0 {
		t.Fatalf("추천 목록에서 사용자를 찾지 못했습니다: popular=%d quiet=%d", popularRank, quietRank)
	}
	if popularRank > quietRank {
		t.Fatalf("Link 수가 많은 사용자가 뒤에 있습니다: popular=%d quiet=%d", popularRank, quietRank)
	}
}

func TestPostgreSQLSearchStillFiltersByText(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}
	repository, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repository.Close)

	suffix := time.Now().UnixNano()
	term := fmt.Sprintf("filterterm%d", suffix)
	viewerID := fmt.Sprintf("usr_filter_viewer_%d", suffix)
	authorID := fmt.Sprintf("usr_filter_author_%d", suffix)
	matchID := fmt.Sprintf("post_filter_match_%d", suffix)
	otherID := fmt.Sprintf("post_filter_other_%d", suffix)
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES
		($1,$1,$1,ARRAY['member']::text[]),($2,$2,$2,ARRAY['member']::text[])`, viewerID, authorID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM posts WHERE id=ANY($1::text[])`, []string{matchID, otherID})
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{viewerID, authorID})
	})
	for id, content := range map[string]string{matchID: term + " 일치", otherID: "전혀 다른 내용"} {
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO posts
			(id,author_id,content,kind,visibility,status,published_at,created_at)
			VALUES($1,$2,$3,'moin','public','published',statement_timestamp(),statement_timestamp())`, id, authorID, content); err != nil {
			t.Fatal(err)
		}
	}

	server := New(repository, nil, "test")
	result := runSearch(t, server, model.User{ID: viewerID}, fmt.Sprintf("q=%s&type=posts&limit=100", term))
	for _, post := range result.Data.Posts {
		if post.ID == otherID {
			t.Fatal("검색어와 무관한 Moin이 결과에 포함되었습니다")
		}
	}
	found := false
	for _, post := range result.Data.Posts {
		if post.ID == matchID {
			found = true
		}
	}
	if !found {
		t.Fatal("검색어와 일치하는 Moin이 결과에 없습니다")
	}
}
