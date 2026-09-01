package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLMoimDerivativesInheritParentConversationScope(t *testing.T) {
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
	ownerID := fmt.Sprintf("usr_moim_owner_%d", suffix)
	memberID := fmt.Sprintf("usr_moim_member_%d", suffix)
	outsiderID := fmt.Sprintf("usr_moim_outsider_%d", suffix)
	moimID := fmt.Sprintf("moim_conversation_%d", suffix)
	moimSlug := fmt.Sprintf("conversation-%d", suffix)
	userIDs := []string{ownerID, memberID, outsiderID}
	for _, userID := range userIDs {
		if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,email,roles) VALUES($1,$1,$1,$1||'@example.com',ARRAY['member']::text[])`, userID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO moims(id,slug,name,description,owner_id,visibility) VALUES($1,$2,'대화 범위 테스트','Echo는 모임 안에 남습니다',$3,'public')`, moimID, moimSlug, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO moim_members(moim_id,user_id,role) VALUES($1,$2,'owner'),($1,$3,'member')`, moimID, ownerID, memberID); err != nil {
		t.Fatal(err)
	}

	var parentID string
	derivativeIDs := []string{}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, postID := range derivativeIDs {
			_, _ = repository.Pool().Exec(ctx, `DELETE FROM outbox_events WHERE idempotency_key LIKE $1`, "notification:post:"+postID+":%")
		}
		postIDs := append([]string{parentID}, derivativeIDs...)
		if parentID != "" {
			_, _ = repository.Pool().Exec(ctx, `DELETE FROM posts WHERE id=ANY($1)`, postIDs)
		}
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM moims WHERE id=$1`, moimID)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=ANY($1)`, userIDs)
	})

	server := New(repository, nil, "v0.1.12-test")
	permissions := []string{"posts:read", "posts:write", "social:write"}
	requestFor := func(userID string) *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/posts", nil)
		user := model.User{ID: userID, Username: userID, DisplayName: userID, Roles: []string{model.RoleMember}, Active: true}
		return request.WithContext(withPrincipal(request, principal{User: user, Permissions: permissions}))
	}

	parent, err := server.createPostRecord(requestFor(ownerID), postInput{
		Content: "모임에서 시작한 원문", Visibility: "moim", MoimID: moimID,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	parentID = parent.ID

	// A stale or malicious client may still send public without moimId. The
	// server must derive both fields from the parent instead of trusting it.
	tests := []struct {
		name       string
		forcedKind string
		input      postInput
	}{
		{name: "Echo", input: postInput{Content: "모임 멤버에게만 보일 Echo", Visibility: "public", ReplyToID: parent.ID}},
		{name: "Quote", input: postInput{Content: "모임 안의 인용", Visibility: "public", QuoteID: parent.ID}},
		{name: "Remoin", forcedKind: "remoin", input: postInput{Visibility: "public", QuoteID: parent.ID}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			post, err := server.createPostRecord(requestFor(memberID), test.input, test.forcedKind)
			if err != nil {
				t.Fatal(err)
			}
			derivativeIDs = append(derivativeIDs, post.ID)
			if post.Visibility != "moim" || post.MoimID != moimID {
				t.Fatalf("%s scope = visibility:%q moim:%q", test.name, post.Visibility, post.MoimID)
			}
			var storedVisibility, storedMoimID string
			if err := repository.Pool().QueryRow(t.Context(), `SELECT visibility,moim_id FROM posts WHERE id=$1`, post.ID).Scan(&storedVisibility, &storedMoimID); err != nil {
				t.Fatal(err)
			}
			if storedVisibility != "moim" || storedMoimID != moimID {
				t.Fatalf("stored %s scope = visibility:%q moim:%q", test.name, storedVisibility, storedMoimID)
			}
			if _, err := server.loadMoin(t.Context(), post.ID, outsiderID); !store.IsNotFound(err) {
				t.Fatalf("outsider load %s error = %v, want not found", test.name, err)
			}
		})
	}
}
