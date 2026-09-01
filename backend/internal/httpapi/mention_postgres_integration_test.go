package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLMentionsHonorVisibilityBlocksAndEditIdempotency(t *testing.T) {
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
	actorID := fmt.Sprintf("usr_mention_actor_%d", suffix)
	followerID := fmt.Sprintf("usr_mention_follower_%d", suffix)
	newFollowerID := fmt.Sprintf("usr_mention_new_%d", suffix)
	outsiderID := fmt.Sprintf("usr_mention_out_%d", suffix)
	blockedID := fmt.Sprintf("usr_mention_block_%d", suffix)
	actorName := fmt.Sprintf("mention_actor_%d", suffix)
	followerName := fmt.Sprintf("mention_follow_%d", suffix)
	newFollowerName := fmt.Sprintf("mention_new_%d", suffix)
	outsiderName := fmt.Sprintf("mention_out_%d", suffix)
	blockedName := fmt.Sprintf("mention_block_%d", suffix)
	users := []struct{ id, username string }{
		{actorID, actorName}, {followerID, followerName}, {newFollowerID, newFollowerName},
		{outsiderID, outsiderName}, {blockedID, blockedName},
	}
	for _, user := range users {
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,email,roles) VALUES($1,$2,$2,$2||'@example.com',ARRAY['member']::text[])`, user.id, user.username); err != nil {
			t.Fatal(err)
		}
	}
	var postID string
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if postID != "" {
			_, _ = repository.Pool().Exec(ctx, `DELETE FROM outbox_events WHERE idempotency_key LIKE $1`, "notification:post:"+postID+":mention:%")
		}
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM audit_events WHERE actor_id=$1`, actorID)
		ids := []string{actorID, followerID, newFollowerID, outsiderID, blockedID}
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=ANY($1)`, ids)
	})
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO follows(follower_id,followee_id) VALUES($1,$3),($2,$3)`, followerID, blockedID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO blocks(blocker_id,blocked_id) VALUES($1,$2)`, blockedID, actorID); err != nil {
		t.Fatal(err)
	}
	server := New(repository, nil, "v0.1.12-test")
	actor := model.User{ID: actorID, Username: actorName, DisplayName: actorName, Roles: []string{model.RoleMember}, Active: true}
	permissions := []string{"posts:read", "posts:write", "social:write"}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/posts", nil)
	request = request.WithContext(withPrincipal(request, principal{User: actor, Permissions: permissions}))
	post, err := server.createPostRecord(request, postInput{
		Content: fmt.Sprintf("@%s @%s @%s", followerName, outsiderName, blockedName), Visibility: "followers",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	postID = post.ID
	if got := mentionRecipients(t, repository, postID); !slices.Equal(got, []string{followerID}) {
		t.Fatalf("initial mention recipients=%v", got)
	}

	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO follows(follower_id,followee_id) VALUES($1,$2)`, newFollowerID, actorID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(updatePostInput{Content: fmt.Sprintf("@%s @%s", followerName, newFollowerName)})
	updateRequest := httptest.NewRequest(http.MethodPatch, "/api/v1/posts/"+postID, bytes.NewReader(body))
	updateRequest.Header.Set("Content-Type", "application/json")
	route := chi.NewRouteContext()
	route.URLParams.Add("postID", postID)
	updateRequest = updateRequest.WithContext(context.WithValue(withPrincipal(updateRequest, principal{User: actor, Permissions: permissions}), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	server.updatePost(response, updateRequest)
	if response.Code != http.StatusOK {
		t.Fatalf("mention edit = %d: %s", response.Code, response.Body.String())
	}
	if got := mentionRecipients(t, repository, postID); !slices.Equal(got, []string{followerID, newFollowerID}) {
		t.Fatalf("edited mention recipients=%v", got)
	}
}

func mentionRecipients(t *testing.T, repository *store.Store, postID string) []string {
	t.Helper()
	rows, err := repository.Pool().Query(t.Context(), `SELECT payload->>'userId' FROM outbox_events WHERE idempotency_key LIKE $1 ORDER BY payload->>'userId'`, "notification:post:"+postID+":mention:%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			t.Fatal(err)
		}
		result = append(result, userID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
