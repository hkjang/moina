package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

const PostAndAuthorColumns = `p.id,p.author_id,p.content,p.kind,p.visibility,p.status,COALESCE(p.reply_to_id,''),COALESCE(p.quote_post_id,''),COALESCE(p.remoin_post_id,''),COALESCE(p.moim_id,''),p.approval_required,p.created_at,p.updated_at,u.id,u.username,u.display_name,u.email,u.bio,u.avatar_id,u.account_type,u.provider,u.roles,u.active,u.created_at,u.updated_at`

type RowScanner interface{ Scan(...any) error }

type Queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type PublishedMoin struct {
	Moin        model.Moin
	PublishedAt time.Time
}

type RankedMoin struct {
	Moin           model.Moin
	Score          float64
	LinkScore      float64
	TopicScore     float64
	DiscoveryScore float64
	RecencyScore   float64
	FollowedTopics int
}

type RankingWeights struct {
	Topic     float64
	Link      float64
	Discovery float64
	Recency   float64
}

func ScanMoin(row RowScanner) (model.Moin, error) {
	var post model.Moin
	err := row.Scan(
		&post.ID, &post.AuthorID, &post.Content, &post.Kind, &post.Visibility, &post.Status,
		&post.ReplyToID, &post.QuoteMoinID, &post.RemoinMoinID, &post.MoimID, &post.ApprovalRequired, &post.CreatedAt, &post.UpdatedAt,
		&post.Author.ID, &post.Author.Username, &post.Author.DisplayName, &post.Author.Email, &post.Author.Bio, &post.Author.AvatarID,
		&post.Author.AccountType, &post.Author.Provider, &post.Author.Roles, &post.Author.Active, &post.Author.CreatedAt, &post.Author.UpdatedAt,
	)
	return post, err
}

// QueryPosts performs one root query and two fixed hydration queries. The SQL
// count is independent of the number of returned posts, including an empty page.
func QueryPosts(ctx context.Context, database Queryer, where []string, args []any, order string, limit, offset int, viewerID string) ([]model.Moin, error) {
	args = append(args, limit, offset)
	query := `SELECT ` + PostAndAuthorColumns + ` FROM posts p JOIN users u ON u.id=p.author_id WHERE ` + strings.Join(where, " AND ") + fmt.Sprintf(` ORDER BY %s LIMIT $%d OFFSET $%d`, order, len(args)-1, len(args))
	rows, err := database.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	items := make([]model.Moin, 0)
	for rows.Next() {
		post, scanErr := ScanMoin(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		items = append(items, post)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := Hydrate(ctx, database, items, viewerID); err != nil {
		return nil, err
	}
	return items, nil
}

// QueryPublishedPosts is the keyset variant used by Following Flow. It keeps
// published_at alongside the public model so the HTTP layer can encode the
// exact database ordering key without changing the REST response model.
func QueryPublishedPosts(ctx context.Context, database Queryer, where []string, args []any, limit, offset int, viewerID string) ([]PublishedMoin, error) {
	args = append(args, limit, offset)
	query := `SELECT ` + PostAndAuthorColumns + fmt.Sprintf(`,p.published_at FROM posts p JOIN users u ON u.id=p.author_id WHERE %s ORDER BY p.published_at DESC,p.id DESC LIMIT $%d OFFSET $%d`, strings.Join(where, " AND "), len(args)-1, len(args))
	rows, err := database.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	items := make([]PublishedMoin, 0)
	for rows.Next() {
		var item PublishedMoin
		if err := rows.Scan(
			&item.Moin.ID, &item.Moin.AuthorID, &item.Moin.Content, &item.Moin.Kind, &item.Moin.Visibility, &item.Moin.Status,
			&item.Moin.ReplyToID, &item.Moin.QuoteMoinID, &item.Moin.RemoinMoinID, &item.Moin.MoimID, &item.Moin.ApprovalRequired, &item.Moin.CreatedAt, &item.Moin.UpdatedAt,
			&item.Moin.Author.ID, &item.Moin.Author.Username, &item.Moin.Author.DisplayName, &item.Moin.Author.Email, &item.Moin.Author.Bio, &item.Moin.Author.AvatarID,
			&item.Moin.Author.AccountType, &item.Moin.Author.Provider, &item.Moin.Author.Roles, &item.Moin.Author.Active, &item.Moin.Author.CreatedAt, &item.Moin.Author.UpdatedAt,
			&item.PublishedAt,
		); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	roots := make([]model.Moin, len(items))
	for index := range items {
		roots[index] = items[index].Moin
	}
	if err := Hydrate(ctx, database, roots, viewerID); err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Moin = roots[index]
	}
	return items, nil
}

// QueryRankedPosts computes the same four explainable For Me components as the
// API response and applies a (score,id) keyset before hydration. This avoids a
// fixed-size candidate window and keeps every page at three SQL round trips.
func QueryRankedPosts(ctx context.Context, database Queryer, where []string, args []any, weights RankingWeights, asOf time.Time, after *After, limit, offset int, viewerID string) ([]RankedMoin, bool, error) {
	args = append(args, asOf.UTC(), weights.Link, weights.Topic, weights.Discovery, weights.Recency, after != nil)
	asOfParam, linkParam, topicParam := len(args)-5, len(args)-4, len(args)-3
	discoveryParam, recencyParam, hasAfterParam := len(args)-2, len(args)-1, len(args)
	afterScore, afterID := 0.0, ""
	if after != nil {
		afterScore, afterID = after.Score, after.ID
	}
	args = append(args, afterScore, afterID, limit+1, offset)
	afterScoreParam, afterIDParam, limitParam, offsetParam := len(args)-3, len(args)-2, len(args)-1, len(args)
	query := `SELECT ` + PostAndAuthorColumns + fmt.Sprintf(`,ranking.value
		FROM posts p JOIN users u ON u.id=p.author_id
		CROSS JOIN LATERAL (
			SELECT
				EXISTS(SELECT 1 FROM follows rf WHERE rf.follower_id=$1 AND rf.followee_id=p.author_id) AS linked,
				(SELECT count(*)::double precision FROM post_topics rpt JOIN user_topic_follows rtf ON rtf.topic_id=rpt.topic_id AND rtf.user_id=$1 WHERE rpt.post_id=p.id) AS followed_topics,
				COALESCE((SELECT sum(CASE WHEN rr.kind IN ('useful','insight','verify') THEN 2 ELSE 1 END)::double precision FROM reactions rr WHERE rr.post_id=p.id),0) AS signal_points
		) metrics
		CROSS JOIN LATERAL (
			SELECT (
				CASE WHEN metrics.linked THEN $%d ELSE 0 END +
				metrics.followed_topics*$%d +
				(metrics.signal_points + CASE WHEN NOT metrics.linked AND metrics.followed_topics=0 THEN 1 ELSE 0 END)*($%d/10.0) +
				CASE WHEN extract(epoch FROM ($%d-p.created_at))/3600.0 < 24 THEN greatest(0,(24-extract(epoch FROM ($%d-p.created_at))/3600.0)/24.0)*$%d ELSE 0 END
			)::double precision AS value
		) ranking
		WHERE %s AND (NOT $%d OR ranking.value<$%d OR ranking.value=$%d AND p.id<$%d)
		ORDER BY ranking.value DESC,p.id DESC LIMIT $%d OFFSET $%d`, linkParam, topicParam, discoveryParam, asOfParam, asOfParam, recencyParam, strings.Join(where, " AND "), hasAfterParam, afterScoreParam, afterScoreParam, afterIDParam, limitParam, offsetParam)
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
			&item.Score,
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

// Hydrate adds media, topics, signals, viewer state, counters, and one level of
// quote/remoin data in exactly two SQL round trips.
func Hydrate(ctx context.Context, database Queryer, roots []model.Moin, viewerID string) error {
	if len(roots) == 0 {
		return nil
	}
	relatedIDs := make([]string, 0)
	seenRelated := make(map[string]bool)
	for index := range roots {
		relatedID := roots[index].QuoteMoinID
		if roots[index].RemoinMoinID != "" {
			relatedID = roots[index].RemoinMoinID
		}
		if relatedID != "" && !seenRelated[relatedID] {
			seenRelated[relatedID] = true
			relatedIDs = append(relatedIDs, relatedID)
		}
	}
	related, err := visibleRelated(ctx, database, relatedIDs, viewerID)
	if err != nil {
		return err
	}
	all := make([]*model.Moin, 0, len(roots)+len(related))
	for index := range roots {
		all = append(all, &roots[index])
	}
	for index := range related {
		all = append(all, &related[index])
	}
	if err := hydrateDetails(ctx, database, all, viewerID); err != nil {
		return err
	}
	relatedByID := make(map[string]model.Moin, len(related))
	for index := range related {
		relatedByID[related[index].ID] = related[index]
	}
	for index := range roots {
		if value, ok := relatedByID[roots[index].RemoinMoinID]; ok {
			copyOfValue := value
			roots[index].RemoinMoin = &copyOfValue
		} else if value, ok := relatedByID[roots[index].QuoteMoinID]; ok {
			copyOfValue := value
			roots[index].QuoteMoin = &copyOfValue
		}
	}
	return nil
}

func visibleRelated(ctx context.Context, database Queryer, ids []string, viewerID string) ([]model.Moin, error) {
	rows, err := database.Query(ctx, `SELECT `+PostAndAuthorColumns+` FROM posts p JOIN users u ON u.id=p.author_id WHERE p.id=ANY($1::text[]) AND p.status='published' AND NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$2 AND b.blocked_id=p.author_id) OR (b.blocker_id=p.author_id AND b.blocked_id=$2)) AND (p.visibility='public' OR p.author_id=$2 OR p.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$2 AND f.followee_id=p.author_id) OR p.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$2))`, ids, viewerID)
	if err != nil {
		return nil, err
	}
	items := make([]model.Moin, 0, len(ids))
	for rows.Next() {
		post, scanErr := ScanMoin(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		items = append(items, post)
	}
	err = rows.Err()
	rows.Close()
	return items, err
}

const detailsSQL = `WITH
media_values AS (
	SELECT pm.post_id,jsonb_agg(jsonb_build_object('id',m.id,'filename',m.filename,'altText',m.alt_text,'mimeType',m.mime_type,'size',m.size_bytes,'width',m.width,'height',m.height,'createdAt',m.created_at) ORDER BY pm.position) AS value
	FROM post_media pm JOIN media_assets m ON m.id=pm.media_id WHERE pm.post_id=ANY($1::text[]) GROUP BY pm.post_id
),
topic_values AS (
	SELECT pt.post_id,jsonb_agg(jsonb_build_object('id',t.id,'slug',t.slug,'name',t.name,'description',t.description,'createdAt',t.created_at,'following',utf.user_id IS NOT NULL) ORDER BY t.name) AS value
	FROM post_topics pt JOIN topics t ON t.id=pt.topic_id LEFT JOIN user_topic_follows utf ON utf.topic_id=t.id AND utf.user_id=$2
	WHERE pt.post_id=ANY($1::text[]) GROUP BY pt.post_id
),
reaction_counts AS (
	SELECT post_id,kind,count(*) AS value FROM reactions WHERE post_id=ANY($1::text[]) GROUP BY post_id,kind
),
signal_values AS (
	SELECT post_id,jsonb_object_agg(kind,value) AS value FROM reaction_counts GROUP BY post_id
),
viewer_signal_values AS (
	SELECT post_id,jsonb_agg(kind ORDER BY kind) AS value FROM reactions WHERE post_id=ANY($1::text[]) AND user_id=$2 GROUP BY post_id
),
reply_counts AS (
	SELECT reply_to_id AS post_id,count(*) AS value FROM posts WHERE reply_to_id=ANY($1::text[]) AND status='published' GROUP BY reply_to_id
),
remoin_counts AS (
	SELECT remoin_post_id AS post_id,count(*) AS value FROM posts WHERE remoin_post_id=ANY($1::text[]) AND status='published' GROUP BY remoin_post_id
),
viewer_remoins AS (
	SELECT remoin_post_id AS post_id FROM posts WHERE remoin_post_id=ANY($1::text[]) AND author_id=$2 AND status<>'deleted' GROUP BY remoin_post_id
)
SELECT p.id,
	COALESCE(mv.value,'[]'::jsonb),COALESCE(tv.value,'[]'::jsonb),COALESCE(sv.value,'{}'::jsonb),COALESCE(vsv.value,'[]'::jsonb),
	COALESCE(rc.value,0),COALESCE(rrc.value,0),bm.post_id IS NOT NULL,fl.followee_id IS NOT NULL,vr.post_id IS NOT NULL
FROM posts p
LEFT JOIN media_values mv ON mv.post_id=p.id
LEFT JOIN topic_values tv ON tv.post_id=p.id
LEFT JOIN signal_values sv ON sv.post_id=p.id
LEFT JOIN viewer_signal_values vsv ON vsv.post_id=p.id
LEFT JOIN reply_counts rc ON rc.post_id=p.id
LEFT JOIN remoin_counts rrc ON rrc.post_id=p.id
LEFT JOIN bookmarks bm ON bm.post_id=p.id AND bm.user_id=$2
LEFT JOIN follows fl ON fl.followee_id=p.author_id AND fl.follower_id=$2
LEFT JOIN viewer_remoins vr ON vr.post_id=p.id
WHERE p.id=ANY($1::text[])`

func hydrateDetails(ctx context.Context, database Queryer, posts []*model.Moin, viewerID string) error {
	ids := make([]string, 0, len(posts))
	byID := make(map[string][]*model.Moin, len(posts))
	for _, post := range posts {
		initialize(post)
		if _, exists := byID[post.ID]; !exists {
			ids = append(ids, post.ID)
		}
		byID[post.ID] = append(byID[post.ID], post)
	}
	rows, err := database.Query(ctx, detailsSQL, ids, viewerID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var mediaRaw, topicsRaw, signalsRaw, viewerSignalsRaw []byte
		var replyCount, remoinCount int64
		var bookmarked, following, viewerRemoined bool
		if err := rows.Scan(&id, &mediaRaw, &topicsRaw, &signalsRaw, &viewerSignalsRaw, &replyCount, &remoinCount, &bookmarked, &following, &viewerRemoined); err != nil {
			rows.Close()
			return err
		}
		targets, ok := byID[id]
		if !ok {
			rows.Close()
			return fmt.Errorf("unexpected hydrated post %q", id)
		}
		if err := applyDetails(targets, mediaRaw, topicsRaw, signalsRaw, viewerSignalsRaw, replyCount, remoinCount, bookmarked, following, viewerRemoined); err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	return err
}

func applyDetails(targets []*model.Moin, mediaRaw, topicsRaw, signalsRaw, viewerSignalsRaw []byte, replyCount, remoinCount int64, bookmarked, following, viewerRemoined bool) error {
	for _, post := range targets {
		if err := json.Unmarshal(mediaRaw, &post.Media); err != nil {
			return err
		}
		if err := json.Unmarshal(topicsRaw, &post.Topics); err != nil {
			return err
		}
		if err := json.Unmarshal(signalsRaw, &post.Signals); err != nil {
			return err
		}
		if err := json.Unmarshal(viewerSignalsRaw, &post.ViewerSignals); err != nil {
			return err
		}
		post.ReplyCount, post.RemoinCount = replyCount, remoinCount
		post.Bookmarked, post.Author.Following, post.ViewerRemoined = bookmarked, following, viewerRemoined
		normalize(post)
	}
	return nil
}

func initialize(post *model.Moin) {
	post.Media = []model.Media{}
	post.Topics = []model.Topic{}
	post.Signals = map[string]int64{}
	post.ViewerSignals = []string{}
	post.Author.Email, post.Author.Provider, post.Author.Roles = "", "", nil
}

func normalize(post *model.Moin) {
	if post.Media == nil {
		post.Media = []model.Media{}
	}
	if post.Topics == nil {
		post.Topics = []model.Topic{}
	}
	if post.Signals == nil {
		post.Signals = map[string]int64{}
	}
	if post.ViewerSignals == nil {
		post.ViewerSignals = []string{}
	}
	for index := range post.Media {
		post.Media[index].URL = "/api/v1/media/" + post.Media[index].ID
		if strings.HasPrefix(post.Media[index].MIMEType, "video/") {
			post.Media[index].Type = "video"
		} else {
			post.Media[index].Type = "image"
		}
	}
	post.Author.Email, post.Author.Provider, post.Author.Roles = "", "", nil
}
