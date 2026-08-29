package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hkjang/moina/backend/internal/store"
)

func TestPostgreSQLFeedSnapshotOwnershipExpiryAndKeyset(t *testing.T) {
	dsn := os.Getenv("MOINA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MOINA_TEST_POSTGRES_DSN is not set")
	}

	ctx := t.Context()
	repository, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	viewerID := fmt.Sprintf("snapshot_viewer_%d", suffix)
	otherViewerID := fmt.Sprintf("snapshot_other_viewer_%d", suffix)
	authorID := fmt.Sprintf("snapshot_author_%d", suffix)
	topicID := fmt.Sprintf("snapshot_topic_%d", suffix)
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{viewerID, otherViewerID, authorID})
		_, _ = repository.Pool().Exec(cleanupContext, `DELETE FROM topics WHERE id=$1`, topicID)
		repository.Close()
	})
	for _, user := range []struct {
		id       string
		username string
	}{
		{viewerID, fmt.Sprintf("snapshot_viewer_%d", suffix)},
		{otherViewerID, fmt.Sprintf("snapshot_other_%d", suffix)},
		{authorID, fmt.Sprintf("snapshot_author_%d", suffix)},
	} {
		if _, err := repository.Pool().Exec(ctx, `INSERT INTO users(id,username,display_name) VALUES($1,$2,$2)`, user.id, user.username); err != nil {
			t.Fatal(err)
		}
	}

	asOf := time.Now().UTC()
	createdAt := asOf.Add(-time.Hour)
	postIDs := make([]string, SnapshotCandidateLimit+5)
	for index := range postIDs {
		postIDs[index] = fmt.Sprintf("snapshot_post_%03d_%d", index, suffix)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO posts(id,author_id,content,status,visibility,created_at,updated_at,published_at)
		SELECT 'snapshot_post_'||lpad(series::text,3,'0')||'_'||$1,$2,'snapshot candidate','published','public',$3,$3,$3
		FROM generate_series(0,$4) series`, fmt.Sprint(suffix), authorID, createdAt, len(postIDs)-1); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO follows(follower_id,followee_id) VALUES($1,$2)`, viewerID, authorID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO topics(id,slug,name) VALUES($1,$2,$2)`, topicID, fmt.Sprintf("snapshot-%d", suffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO user_topic_follows(user_id,topic_id) VALUES($1,$2)`, viewerID, topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO post_topics(post_id,topic_id) VALUES($1,$2)`, postIDs[len(postIDs)-1], topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO reactions(user_id,post_id,kind) VALUES($1,$2,'insight')`, otherViewerID, postIDs[len(postIDs)-1]); err != nil {
		t.Fatal(err)
	}

	whereAtCreation := []string{
		"p.status='published'",
		"p.visibility='public'",
		"p.author_id=$2",
		"p.published_at IS NOT NULL",
		"p.published_at<=$3",
	}
	queryWhere := []string{
		"p.status='published'",
		"p.visibility='public'",
		"p.author_id=$2",
		"p.published_at IS NOT NULL",
	}
	snapshotID := fmt.Sprintf("snapshot_%d", suffix)
	preferenceHash := fmt.Sprintf("preferences_%d", suffix)
	preferences := json.RawMessage(`{"topicWeight":30,"linkWeight":20,"discoveryWeight":10,"recencyWeight":24,"excludedTopics":[],"showReasons":true}`)
	weights := RankingWeights{Topic: 30, Link: 20, Discovery: 10, Recency: 24}
	mismatchedCreateID := fmt.Sprintf("snapshot_mismatched_create_%d", suffix)
	if _, mismatchErr := CreateOrReuseRankedSnapshot(ctx, repository.Pool(), mismatchedCreateID, otherViewerID, RankingVersion, preferenceHash+"_mismatch", preferences,
		whereAtCreation, []any{viewerID, authorID}, weights, asOf, asOf.Add(time.Hour)); !errors.Is(mismatchErr, ErrSnapshotUnavailable) {
		t.Fatalf("mismatched create owner error = %v", mismatchErr)
	}
	var mismatchedCreateExists bool
	if err := repository.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM feed_snapshots WHERE id=$1)`, mismatchedCreateID).Scan(&mismatchedCreateExists); err != nil {
		t.Fatal(err)
	}
	if mismatchedCreateExists {
		t.Fatal("mismatched create owner wrote a snapshot")
	}
	metadata, err := CreateOrReuseRankedSnapshot(ctx, repository.Pool(), snapshotID, viewerID, RankingVersion, preferenceHash, preferences,
		whereAtCreation, []any{viewerID, authorID}, weights, asOf, asOf.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ID != snapshotID || metadata.Reused || !timeCloseEnough(metadata.AsOf, asOf) || !jsonEqual(metadata.Preferences, preferences) {
		t.Fatalf("created metadata = %+v, preferences = %s", metadata, metadata.Preferences)
	}

	loaded, available, err := Snapshot(ctx, repository.Pool(), snapshotID, viewerID, RankingVersion)
	if err != nil || !available || loaded.ID != snapshotID || !timeCloseEnough(loaded.AsOf, asOf) || !jsonEqual(loaded.Preferences, preferences) {
		t.Fatalf("owner snapshot = %+v, available = %v, err = %v", loaded, available, err)
	}
	_, available, err = Snapshot(ctx, repository.Pool(), snapshotID, otherViewerID, RankingVersion)
	if err != nil || available {
		t.Fatalf("other-user availability = %v, err = %v", available, err)
	}
	mismatchedOwnerPage, _, err := QueryRankedSnapshot(ctx, repository.Pool(), snapshotID, RankingVersion,
		queryWhere, []any{viewerID, authorID}, nil, 2, otherViewerID)
	if !errors.Is(err, ErrSnapshotUnavailable) || len(mismatchedOwnerPage) != 0 {
		t.Fatalf("mismatched owner arguments page = %v, err = %v", rankedIDs(mismatchedOwnerPage), err)
	}

	reused, err := CreateOrReuseRankedSnapshot(ctx, repository.Pool(), fmt.Sprintf("snapshot_reuse_%d", suffix), viewerID, RankingVersion, preferenceHash, preferences,
		whereAtCreation, []any{viewerID, authorID}, weights, asOf, asOf.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != snapshotID || !reused.Reused || !timeCloseEnough(reused.AsOf, asOf) || !jsonEqual(reused.Preferences, preferences) {
		t.Fatalf("reused metadata = %+v", reused)
	}
	var snapshotCount, itemCount int
	if err := repository.Pool().QueryRow(ctx, `SELECT count(*),(SELECT count(*) FROM feed_snapshot_items WHERE snapshot_id=$2) FROM feed_snapshots WHERE user_id=$1`, viewerID, snapshotID).Scan(&snapshotCount, &itemCount); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 1 || itemCount != SnapshotCandidateLimit {
		t.Fatalf("reuse snapshots = %d, candidates = %d", snapshotCount, itemCount)
	}
	var storedHash string
	var storedPreferences json.RawMessage
	if err := repository.Pool().QueryRow(ctx, `SELECT preference_hash,preferences FROM feed_snapshots WHERE id=$1`, snapshotID).Scan(&storedHash, &storedPreferences); err != nil {
		t.Fatal(err)
	}
	if storedHash != preferenceHash || !jsonEqual(storedPreferences, preferences) {
		t.Fatalf("stored hash/preferences = %q/%s", storedHash, storedPreferences)
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO feed_snapshot_items(snapshot_id,post_id,score,link_score,topic_score,discovery_score,recency_score,followed_topics) VALUES($1,$2,$3,0,0,0,0,0)`, snapshotID, postIDs[0], math.NaN()); err == nil {
		t.Fatal("snapshot item accepted a NaN score")
	}
	if _, err := repository.Pool().Exec(ctx, `INSERT INTO feed_snapshots(id,user_id,ranking_version,preference_hash,preferences,reuse_bucket,as_of,expires_at) VALUES($1,$2,$3,$4,$5,0,$6,$6)`, fmt.Sprintf("snapshot_invalid_expiry_%d", suffix), viewerID, RankingVersion, preferenceHash+"_invalid", preferences, asOf); err == nil {
		t.Fatal("snapshot accepted expires_at <= as_of")
	}

	if _, err := repository.Pool().Exec(ctx, `DELETE FROM follows WHERE follower_id=$1 AND followee_id=$2`, viewerID, authorID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `DELETE FROM user_topic_follows WHERE user_id=$1 AND topic_id=$2`, viewerID, topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Pool().Exec(ctx, `DELETE FROM reactions WHERE user_id=$1 AND post_id=$2`, otherViewerID, postIDs[len(postIDs)-1]); err != nil {
		t.Fatal(err)
	}

	first, hasMore, err := QueryRankedSnapshot(ctx, repository.Pool(), snapshotID, RankingVersion,
		queryWhere, []any{viewerID, authorID}, nil, 2, viewerID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || !slices.Equal(rankedIDs(first), []string{postIDs[len(postIDs)-1], postIDs[len(postIDs)-2]}) {
		t.Fatalf("first page ids = %v, hasMore = %v", rankedIDs(first), hasMore)
	}
	if first[0].FollowedTopics != 1 || !closeEnough(first[0].LinkScore, 20) || !closeEnough(first[0].TopicScore, 30) || !closeEnough(first[0].DiscoveryScore, 2) || !closeEnough(first[0].RecencyScore, 23) || !closeEnough(first[0].Score, 75) {
		t.Fatalf("frozen score components changed after graph mutation: %+v", first[0])
	}
	if !closeEnough(first[0].Score, first[0].LinkScore+first[0].TopicScore+first[0].DiscoveryScore+first[0].RecencyScore) {
		t.Fatalf("component sum does not equal score: %+v", first[0])
	}
	second, hasMore, err := QueryRankedSnapshot(ctx, repository.Pool(), snapshotID, RankingVersion,
		queryWhere, []any{viewerID, authorID}, &After{Score: first[1].Score, ID: first[1].Moin.ID}, 2, viewerID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || !slices.Equal(rankedIDs(second), []string{postIDs[len(postIDs)-3], postIDs[len(postIDs)-4]}) {
		t.Fatalf("second page ids = %v, hasMore = %v", rankedIDs(second), hasMore)
	}

	otherUserPage, _, err := QueryRankedSnapshot(ctx, repository.Pool(), snapshotID, RankingVersion,
		queryWhere, []any{otherViewerID, authorID}, nil, 10, otherViewerID)
	if !errors.Is(err, ErrSnapshotUnavailable) || len(otherUserPage) != 0 {
		t.Fatalf("cross-owner query page = %v, err = %v", rankedIDs(otherUserPage), err)
	}

	if _, err := repository.Pool().Exec(ctx, `UPDATE feed_snapshots SET as_of=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE id=$1`, snapshotID); err != nil {
		t.Fatal(err)
	}
	_, available, err = Snapshot(ctx, repository.Pool(), snapshotID, viewerID, RankingVersion)
	if err != nil || available {
		t.Fatalf("expired availability = %v, err = %v", available, err)
	}
	expiredPage, _, err := QueryRankedSnapshot(ctx, repository.Pool(), snapshotID, RankingVersion,
		queryWhere, []any{viewerID, authorID}, nil, 10, viewerID)
	if !errors.Is(err, ErrSnapshotUnavailable) || len(expiredPage) != 0 {
		t.Fatalf("expired query page = %v, err = %v", rankedIDs(expiredPage), err)
	}

	replacementID := fmt.Sprintf("snapshot_replacement_%d", suffix)
	replacementAsOf := time.Now().UTC()
	replacement, err := CreateOrReuseRankedSnapshot(ctx, repository.Pool(), replacementID, viewerID, RankingVersion, preferenceHash, preferences,
		whereAtCreation, []any{viewerID, authorID}, weights, replacementAsOf, replacementAsOf.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID != replacementID || replacement.Reused {
		t.Fatalf("replacement metadata = %+v", replacement)
	}
	var expiredStillExists bool
	if err := repository.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM feed_snapshots WHERE id=$1)`, snapshotID).Scan(&expiredStillExists); err != nil {
		t.Fatal(err)
	}
	if expiredStillExists {
		t.Fatal("expired data-modifying CTE did not clean the previous snapshot")
	}

	for index := 0; index < 4; index++ {
		candidateAsOf := time.Now().UTC()
		candidateID := fmt.Sprintf("snapshot_limited_%d_%d", index, suffix)
		candidateHash := fmt.Sprintf("preferences_limited_%d_%d", index, suffix)
		if _, err := CreateOrReuseRankedSnapshot(ctx, repository.Pool(), candidateID, viewerID, RankingVersion, candidateHash, preferences,
			whereAtCreation, []any{viewerID, authorID}, weights, candidateAsOf, candidateAsOf.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	var activeSnapshots, totalSnapshots, oversizedSnapshots int
	if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FILTER(WHERE expires_at>now()),count(*),(SELECT count(*) FROM (SELECT snapshot_id,count(*) AS n FROM feed_snapshot_items GROUP BY snapshot_id HAVING count(*)>$2) oversized) FROM feed_snapshots WHERE user_id=$1`, viewerID, SnapshotCandidateLimit).Scan(&activeSnapshots, &totalSnapshots, &oversizedSnapshots); err != nil {
		t.Fatal(err)
	}
	if activeSnapshots > 3 || totalSnapshots > 3 || oversizedSnapshots != 0 {
		t.Fatalf("snapshot bounds violated: active=%d total=%d oversized=%d", activeSnapshots, totalSnapshots, oversizedSnapshots)
	}

	const concurrentSnapshots = 8
	start := make(chan struct{})
	errorsByWorker := make(chan error, concurrentSnapshots)
	var workers sync.WaitGroup
	for index := 0; index < concurrentSnapshots; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			candidateAsOf := time.Now().UTC()
			_, workerErr := CreateOrReuseRankedSnapshot(ctx, repository.Pool(), fmt.Sprintf("snapshot_concurrent_%d_%d", worker, suffix), viewerID, RankingVersion,
				fmt.Sprintf("preferences_concurrent_%d_%d", worker, suffix), preferences,
				whereAtCreation, []any{viewerID, authorID}, weights, candidateAsOf, candidateAsOf.Add(time.Hour))
			errorsByWorker <- workerErr
		}(index)
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)
	successfulWorkers := 0
	for workerErr := range errorsByWorker {
		if workerErr == nil {
			successfulWorkers++
			continue
		}
		if !errors.Is(workerErr, ErrSnapshotBusy) {
			t.Fatal(workerErr)
		}
	}
	if successfulWorkers == 0 {
		t.Fatal("all concurrent snapshot requests were busy")
	}
	if err := repository.Pool().QueryRow(ctx, `SELECT count(*) FILTER(WHERE expires_at>now()),count(*) FROM feed_snapshots WHERE user_id=$1`, viewerID).Scan(&activeSnapshots, &totalSnapshots); err != nil {
		t.Fatal(err)
	}
	if activeSnapshots > 3 || totalSnapshots > 3 {
		t.Fatalf("concurrent snapshot bound violated: active=%d total=%d", activeSnapshots, totalSnapshots)
	}
}

func rankedIDs(items []RankedMoin) []string {
	ids := make([]string, len(items))
	for index := range items {
		ids[index] = items[index].Moin.ID
	}
	return ids
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func closeEnough(left, right float64) bool {
	return math.Abs(left-right) < 0.000001
}

func timeCloseEnough(left, right time.Time) bool {
	difference := left.Sub(right)
	return difference > -time.Microsecond && difference < time.Microsecond
}
