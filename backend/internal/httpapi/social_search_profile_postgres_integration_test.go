package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLSearchTypeScopesResponseArrays(t *testing.T) {
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
	term := fmt.Sprintf("searchscope%d", suffix)
	viewerID := fmt.Sprintf("usr_search_scope_viewer_%d", suffix)
	userID := fmt.Sprintf("usr_search_scope_result_%d", suffix)
	postID := fmt.Sprintf("post_search_scope_%d", suffix)
	topicID := fmt.Sprintf("topic_search_scope_%d", suffix)
	moimID := fmt.Sprintf("moim_search_scope_%d", suffix)
	topicSlug := term + "-topic"
	moimSlug := term + "-moim"

	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES
		($1,$1,'Search Scope Viewer',ARRAY['member']::text[]),
		($2,$3,$3,ARRAY['member']::text[])`, viewerID, userID, term+"user"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM moims WHERE id=$1`, moimID)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM posts WHERE id=$1`, postID)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM topics WHERE id=$1`, topicID)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{viewerID, userID})
	})
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO posts
		(id,author_id,content,kind,visibility,status,published_at,created_at)
		VALUES($1,$2,$3,'moin','public','published',statement_timestamp(),statement_timestamp())`, postID, userID, term+" post"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO topics(id,slug,name) VALUES($1,$2,$3)`, topicID, topicSlug, term+" topic"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO moims(id,slug,name,description,owner_id,visibility)
		VALUES($1,$2,$3,$3,$4,'public')`, moimID, moimSlug, term+" moim", userID); err != nil {
		t.Fatal(err)
	}

	server := New(repository, nil, "test")
	wantIDs := map[string]string{
		"posts":  postID,
		"users":  userID,
		"topics": topicID,
		"moims":  moimID,
	}
	for _, searchType := range []string{"posts", "users", "topics", "moims"} {
		t.Run(searchType, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/search?q="+url.QueryEscape(term)+"&type="+searchType+"&limit=10", nil)
			request = request.WithContext(withPrincipal(request, principal{User: model.User{ID: viewerID}}))
			response := httptest.NewRecorder()
			server.search(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("search type=%s status=%d body=%s", searchType, response.Code, response.Body.String())
			}
			var envelope socialSearchContractEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			arrays := map[string]json.RawMessage{
				"posts": envelope.Data.Posts, "users": envelope.Data.Users,
				"topics": envelope.Data.Topics, "moims": envelope.Data.Moims,
			}
			for name, raw := range arrays {
				if name == searchType {
					assertSocialSearchResultID(t, raw, wantIDs[name])
					continue
				}
				if string(raw) != "[]" {
					t.Errorf("search type=%s returned %s=%s, want []", searchType, name, raw)
				}
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/search?q="+url.QueryEscape(term)+"&type=unsupported", nil)
	request = request.WithContext(withPrincipal(request, principal{User: model.User{ID: viewerID}}))
	response := httptest.NewRecorder()
	server.search(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid search type status=%d body=%s", response.Code, response.Body.String())
	}
	var apiError struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
		t.Fatal(err)
	}
	if apiError.Code != "invalid_type" {
		t.Fatalf("invalid search type code=%q, want invalid_type", apiError.Code)
	}
}

func TestPostgreSQLProfileCountsOnlyPublishedPublicActivity(t *testing.T) {
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
	viewerID := fmt.Sprintf("usr_profile_count_viewer_%d", suffix)
	reactorID := fmt.Sprintf("usr_profile_count_reactor_%d", suffix)
	authorID := fmt.Sprintf("usr_profile_count_author_%d", suffix)
	postPrefix := fmt.Sprintf("post_profile_count_%d_", suffix)
	publicFirst := postPrefix + "public_first"
	publicSecond := postPrefix + "public_second"
	followersPost := postPrefix + "followers"
	pendingPost := postPrefix + "pending"
	deletedPost := postPrefix + "deleted"

	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES
		($1,$1,$1,ARRAY['member']::text[]),
		($2,$2,$2,ARRAY['member']::text[]),
		($3,$3,$3,ARRAY['member']::text[])`, viewerID, reactorID, authorID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{viewerID, reactorID, authorID})
	})
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO posts
		(id,author_id,content,kind,visibility,status,published_at,created_at) VALUES
		($1,$6,'public first','moin','public','published',statement_timestamp(),statement_timestamp()),
		($2,$6,'public second','moin','public','published',statement_timestamp(),statement_timestamp()),
		($3,$6,'followers only','moin','followers','published',statement_timestamp(),statement_timestamp()),
		($4,$6,'pending public','moin','public','pending_approval',NULL,statement_timestamp()),
		($5,$6,'deleted public','moin','public','deleted',statement_timestamp(),statement_timestamp())`,
		publicFirst, publicSecond, followersPost, pendingPost, deletedPost, authorID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO reactions(user_id,post_id,kind) VALUES
		($1,$3,'like'),($2,$3,'insight'),
		($1,$4,'like'),($1,$5,'useful'),($1,$6,'verify')`,
		viewerID, reactorID, publicFirst, followersPost, pendingPost, deletedPost); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/"+url.PathEscape(authorID), nil)
	request = request.WithContext(withPrincipal(request, principal{User: model.User{ID: viewerID}}))
	response := httptest.NewRecorder()
	New(repository, nil, "test").writeProfile(response, request, authorID)
	if response.Code != http.StatusOK {
		t.Fatalf("profile status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			ID        string `json:"id"`
			Signal    int64  `json:"signal"`
			MoinCount int64  `json:"moinCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.ID != authorID {
		t.Fatalf("profile id=%q, want %q", envelope.Data.ID, authorID)
	}
	if envelope.Data.Signal != 2 {
		t.Errorf("profile signal=%d, want 2 reactions on published public Moin", envelope.Data.Signal)
	}
	if envelope.Data.MoinCount != 2 {
		t.Errorf("profile moinCount=%d, want 2 published public Moin", envelope.Data.MoinCount)
	}
}

type socialSearchContractEnvelope struct {
	Data struct {
		Posts  json.RawMessage `json:"posts"`
		Users  json.RawMessage `json:"users"`
		Topics json.RawMessage `json:"topics"`
		Moims  json.RawMessage `json:"moims"`
	} `json:"data"`
}

func assertSocialSearchResultID(t *testing.T, raw json.RawMessage, wantID string) {
	t.Helper()
	var items []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("decode search results %s: %v", raw, err)
	}
	if len(items) != 1 || items[0].ID != wantID {
		t.Fatalf("search results=%s, want one item id=%q", raw, wantID)
	}
}
