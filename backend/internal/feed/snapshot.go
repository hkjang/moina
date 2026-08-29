package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

const (
	SnapshotCandidateLimit = 200
	snapshotReuseSeconds   = int64(30)
)

var (
	ErrSnapshotUnavailable = errors.New("feed snapshot is unavailable")
	ErrSnapshotBusy        = errors.New("feed snapshot creation is busy")
)

type SnapshotMetadata struct {
	ID          string
	AsOf        time.Time
	Preferences json.RawMessage
	Reused      bool
}

type snapshotBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// CreateOrReuseRankedSnapshot freezes at most 200 candidates and every score
// component in one SQL statement. A user retains at most three active
// snapshots; identical preferences reuse one snapshot per 30-second bucket.
func CreateOrReuseRankedSnapshot(ctx context.Context, database Queryer, preferredID, userID, rankingVersion, preferenceHash string, preferences json.RawMessage, where []string, args []any, weights RankingWeights, asOf, expiresAt time.Time) (SnapshotMetadata, error) {
	if database == nil || strings.TrimSpace(preferredID) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(rankingVersion) == "" || strings.TrimSpace(preferenceHash) == "" || !json.Valid(preferences) || asOf.IsZero() || !expiresAt.After(asOf) || !snapshotOwnerMatches(args, userID) {
		return SnapshotMetadata{}, ErrSnapshotUnavailable
	}
	beginner, ok := database.(snapshotBeginner)
	if !ok {
		return SnapshotMetadata{}, errors.New("feed snapshot database does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return SnapshotMetadata{}, fmt.Errorf("feed snapshot transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,1297042026))`, userID).Scan(&locked); err != nil {
		return SnapshotMetadata{}, fmt.Errorf("feed snapshot lock: %w", err)
	}
	if !locked {
		return SnapshotMetadata{}, ErrSnapshotBusy
	}
	args = append(args, asOf.UTC(), weights.Link, weights.Topic, weights.Discovery, weights.Recency, preferredID, userID, rankingVersion, expiresAt.UTC(), preferenceHash, string(preferences), asOf.Unix()/snapshotReuseSeconds, SnapshotCandidateLimit)
	asOfParam, linkParam, topicParam := len(args)-12, len(args)-11, len(args)-10
	discoveryParam, recencyParam := len(args)-9, len(args)-8
	snapshotParam, userParam, versionParam := len(args)-7, len(args)-6, len(args)-5
	expiryParam, hashParam, preferencesParam := len(args)-4, len(args)-3, len(args)-2
	bucketParam, candidateLimitParam := len(args)-1, len(args)
	query := fmt.Sprintf(`WITH pruned AS (
		DELETE FROM feed_snapshots fs
		WHERE fs.user_id=$%d AND (
			fs.expires_at<=now() OR fs.id IN (
				SELECT old.id FROM feed_snapshots old
				WHERE old.user_id=$%d AND old.expires_at>now()
				ORDER BY old.created_at DESC,old.id DESC OFFSET 2
			)
		)
		RETURNING fs.id
	), chosen AS (
		INSERT INTO feed_snapshots(id,user_id,ranking_version,preference_hash,preferences,reuse_bucket,as_of,expires_at)
		SELECT $%d,$%d,$%d,$%d,$%d::jsonb,$%d,$%d,$%d FROM (SELECT count(*) FROM pruned) cleanup
		ON CONFLICT(user_id,ranking_version,preference_hash,reuse_bucket) DO UPDATE
		SET expires_at=greatest(feed_snapshots.expires_at,EXCLUDED.expires_at)
		RETURNING feed_snapshots.id,feed_snapshots.as_of,feed_snapshots.preferences,(feed_snapshots.id=$%d) AS created
	), candidates AS (
		SELECT p.id,p.author_id,p.created_at
		FROM posts p JOIN users u ON u.id=p.author_id
		WHERE %s
		ORDER BY p.published_at DESC,p.id DESC LIMIT $%d
	), ranked_base AS (
		SELECT p.id,
			CASE WHEN metrics.linked THEN $%d ELSE 0 END::double precision AS link_score,
			(metrics.followed_topics*$%d)::double precision AS topic_score,
			((metrics.signal_points+CASE WHEN NOT metrics.linked AND metrics.followed_topics=0 THEN 1 ELSE 0 END)*($%d/10.0))::double precision AS discovery_score,
			(CASE WHEN extract(epoch FROM ($%d-p.created_at))/3600.0 < 24 THEN greatest(0,(24-extract(epoch FROM ($%d-p.created_at))/3600.0)/24.0)*$%d ELSE 0 END)::double precision AS recency_score,
			metrics.followed_topics::integer AS followed_topics
		FROM candidates p
		CROSS JOIN LATERAL (
			SELECT
				EXISTS(SELECT 1 FROM follows rf WHERE rf.follower_id=$1 AND rf.followee_id=p.author_id) AS linked,
				(SELECT count(*)::double precision FROM post_topics rpt JOIN user_topic_follows rtf ON rtf.topic_id=rpt.topic_id AND rtf.user_id=$1 WHERE rpt.post_id=p.id) AS followed_topics,
				COALESCE((SELECT sum(CASE WHEN rr.kind IN ('useful','insight','verify') THEN 2 ELSE 1 END)::double precision FROM reactions rr WHERE rr.post_id=p.id),0) AS signal_points
		) metrics
	), ranked AS (
		SELECT ranked_base.*,(link_score+topic_score+discovery_score+recency_score)::double precision AS score
		FROM ranked_base ORDER BY score DESC,id DESC
	), inserted AS (
		INSERT INTO feed_snapshot_items(snapshot_id,post_id,score,link_score,topic_score,discovery_score,recency_score,followed_topics)
		SELECT chosen.id,ranked.id,ranked.score,ranked.link_score,ranked.topic_score,ranked.discovery_score,ranked.recency_score,ranked.followed_topics
		FROM chosen CROSS JOIN ranked WHERE chosen.created
		ON CONFLICT(snapshot_id,post_id) DO NOTHING
		RETURNING post_id
	)
	SELECT chosen.id,chosen.as_of,chosen.preferences,NOT chosen.created,count(inserted.post_id)
	FROM chosen LEFT JOIN inserted ON true
	GROUP BY chosen.id,chosen.as_of,chosen.preferences,chosen.created`, userParam, userParam,
		snapshotParam, userParam, versionParam, hashParam, preferencesParam, bucketParam, asOfParam, expiryParam, snapshotParam,
		strings.Join(where, " AND "), candidateLimitParam, linkParam, topicParam, discoveryParam, asOfParam, asOfParam, recencyParam)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return SnapshotMetadata{}, fmt.Errorf("create feed snapshot: %w", err)
	}
	if !rows.Next() {
		rows.Close()
		if err := rows.Err(); err != nil {
			return SnapshotMetadata{}, fmt.Errorf("create feed snapshot: %w", err)
		}
		return SnapshotMetadata{}, ErrSnapshotUnavailable
	}
	var metadata SnapshotMetadata
	var inserted int64
	if err := rows.Scan(&metadata.ID, &metadata.AsOf, &metadata.Preferences, &metadata.Reused, &inserted); err != nil {
		rows.Close()
		return SnapshotMetadata{}, fmt.Errorf("create feed snapshot: %w", err)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return SnapshotMetadata{}, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return SnapshotMetadata{}, fmt.Errorf("feed snapshot commit: %w", err)
	}
	return metadata, nil
}

func Snapshot(ctx context.Context, database Queryer, snapshotID, userID, rankingVersion string) (SnapshotMetadata, bool, error) {
	rows, err := database.Query(ctx, `SELECT id,as_of,preferences FROM feed_snapshots WHERE id=$1 AND user_id=$2 AND ranking_version=$3 AND expires_at>now()`, snapshotID, userID, rankingVersion)
	if err != nil {
		return SnapshotMetadata{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return SnapshotMetadata{}, false, err
		}
		return SnapshotMetadata{}, false, nil
	}
	var metadata SnapshotMetadata
	if err := rows.Scan(&metadata.ID, &metadata.AsOf, &metadata.Preferences); err != nil {
		return SnapshotMetadata{}, false, err
	}
	return metadata, true, rows.Err()
}

// QueryRankedSnapshot applies the stable (score,id) keyset to frozen scores,
// while rechecking current block, mute, visibility and publication rules.
func QueryRankedSnapshot(ctx context.Context, database Queryer, snapshotID, rankingVersion string, where []string, args []any, after *After, limit int, viewerID string) ([]RankedMoin, bool, error) {
	if !snapshotOwnerMatches(args, viewerID) {
		return nil, false, ErrSnapshotUnavailable
	}
	afterScore, afterID := 0.0, ""
	if after != nil {
		afterScore, afterID = after.Score, after.ID
	}
	args = append(args, snapshotID, rankingVersion, after != nil, afterScore, afterID, limit+1)
	snapshotParam, versionParam, hasAfterParam := len(args)-5, len(args)-4, len(args)-3
	afterScoreParam, afterIDParam, limitParam := len(args)-2, len(args)-1, len(args)
	query := `SELECT ` + PostAndAuthorColumns + fmt.Sprintf(`,item.score,item.link_score,item.topic_score,item.discovery_score,item.recency_score,item.followed_topics
		FROM feed_snapshot_items item
		JOIN feed_snapshots snapshot ON snapshot.id=item.snapshot_id
		JOIN posts p ON p.id=item.post_id
		JOIN users u ON u.id=p.author_id
		WHERE snapshot.id=$%d AND snapshot.user_id=$1 AND snapshot.ranking_version=$%d AND snapshot.expires_at>now()
		AND %s
		AND (NOT $%d OR item.score<$%d OR item.score=$%d AND p.id<$%d)
		ORDER BY item.score DESC,p.id DESC LIMIT $%d`, snapshotParam, versionParam, strings.Join(where, " AND "), hasAfterParam, afterScoreParam, afterScoreParam, afterIDParam, limitParam)
	rows, err := database.Query(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	items := make([]RankedMoin, 0, limit+1)
	for rows.Next() {
		var item RankedMoin
		if err := rows.Scan(
			&item.Moin.ID, &item.Moin.AuthorID, &item.Moin.Content, &item.Moin.Kind, &item.Moin.Visibility, &item.Moin.Status,
			&item.Moin.ReplyToID, &item.Moin.QuoteMoinID, &item.Moin.RemoinMoinID, &item.Moin.MoimID, &item.Moin.ApprovalRequired, &item.Moin.CreatedAt, &item.Moin.UpdatedAt,
			&item.Moin.Author.ID, &item.Moin.Author.Username, &item.Moin.Author.DisplayName, &item.Moin.Author.Email, &item.Moin.Author.Bio, &item.Moin.Author.AvatarID,
			&item.Moin.Author.AccountType, &item.Moin.Author.Provider, &item.Moin.Author.Roles, &item.Moin.Author.Active, &item.Moin.Author.CreatedAt, &item.Moin.Author.UpdatedAt,
			&item.Score, &item.LinkScore, &item.TopicScore, &item.DiscoveryScore, &item.RecencyScore, &item.FollowedTopics,
		); err != nil {
			rows.Close()
			return nil, false, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	if len(items) == 0 {
		if _, available, checkErr := Snapshot(ctx, database, snapshotID, viewerID, rankingVersion); checkErr != nil {
			return nil, false, checkErr
		} else if !available {
			return nil, false, ErrSnapshotUnavailable
		}
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	roots := make([]model.Moin, len(items))
	for index := range items {
		roots[index] = items[index].Moin
	}
	if err := Hydrate(ctx, database, roots, viewerID); err != nil {
		return nil, false, err
	}
	for index := range items {
		items[index].Moin = roots[index]
	}
	return items, hasMore, nil
}

func snapshotOwnerMatches(args []any, ownerID string) bool {
	if len(args) == 0 {
		return false
	}
	viewerID, ok := args[0].(string)
	return ok && viewerID == ownerID
}
