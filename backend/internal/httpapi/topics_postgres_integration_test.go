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

func TestPostgreSQLTopicPulseRanksRecentPublicActivity(t *testing.T) {
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
	viewerID := fmt.Sprintf("usr_topic_pulse_%d", suffix)
	topicPrefix := fmt.Sprintf("topic_pulse_%d", suffix)
	topicRecent := topicPrefix + "_recent"
	topicWeek := topicPrefix + "_week"
	topicPrivate := topicPrefix + "_private"
	topicOld := topicPrefix + "_old"
	postRecent := fmt.Sprintf("post_pulse_recent_%d", suffix)
	postWeek := fmt.Sprintf("post_pulse_week_%d", suffix)
	postPrivate := fmt.Sprintf("post_pulse_private_%d", suffix)
	postOld := fmt.Sprintf("post_pulse_old_%d", suffix)
	topicIDs := []string{topicRecent, topicWeek, topicPrivate, topicOld}

	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO users(id,username,display_name,roles) VALUES($1,$1,$1,ARRAY['member']::text[])`, viewerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, viewerID)
		_, _ = repository.Pool().Exec(ctx, `DELETE FROM topics WHERE id=ANY($1::text[])`, topicIDs)
	})
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO topics(id,slug,name) VALUES
		($1,$1,'Recent Pulse'),($2,$2,'Week Pulse'),($3,$3,'Private Pulse'),($4,$4,'Old Pulse')`, topicRecent, topicWeek, topicPrivate, topicOld); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO posts(id,author_id,content,kind,visibility,status,published_at,created_at) VALUES
		($1,$5,'recent','moin','public','published',statement_timestamp()-interval '1 hour',statement_timestamp()-interval '1 hour'),
		($2,$5,'week','moin','public','published',statement_timestamp()-interval '3 days',statement_timestamp()-interval '3 days'),
		($3,$5,'private','moin','followers','published',statement_timestamp()-interval '1 hour',statement_timestamp()-interval '1 hour'),
		($4,$5,'old','moin','public','published',statement_timestamp()-interval '8 days',statement_timestamp()-interval '8 days')`, postRecent, postWeek, postPrivate, postOld, viewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO post_topics(post_id,topic_id) VALUES($1,$5),($2,$6),($3,$7),($4,$8)`, postRecent, postWeek, postPrivate, postOld, topicRecent, topicWeek, topicPrivate, topicOld); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(t.Context(), `INSERT INTO reactions(user_id,post_id,kind,created_at) VALUES($1,$2,'insight',statement_timestamp()-interval '30 minutes')`, viewerID, postRecent); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/topics?sort=trending&limit=10&q="+topicPrefix, nil)
	request = request.WithContext(withPrincipal(request, principal{User: model.User{ID: viewerID}}))
	recorder := httptest.NewRecorder()
	New(repository, nil, "test").listTopics(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("Topic Pulse status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			Items []model.Topic `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Items) != 2 {
		t.Fatalf("Pulse topics=%+v, want only two recent public topics", response.Data.Items)
	}
	if response.Data.Items[0].ID != topicRecent || response.Data.Items[0].TrendScore != 10 || response.Data.Items[0].MoinCount != 1 {
		t.Fatalf("recent Pulse=%+v", response.Data.Items[0])
	}
	if response.Data.Items[1].ID != topicWeek || response.Data.Items[1].TrendScore != 2 || response.Data.Items[1].MoinCount != 1 {
		t.Fatalf("week Pulse=%+v", response.Data.Items[1])
	}
}
