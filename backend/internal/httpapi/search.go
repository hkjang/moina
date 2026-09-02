package httpapi

import (
	"context"

	feedservice "github.com/hkjang/moina/backend/internal/feed"
	"github.com/hkjang/moina/backend/internal/model"
	searchservice "github.com/hkjang/moina/backend/internal/search"
	"github.com/hkjang/moina/backend/internal/visibility"
)

// Search runs in one of two modes. With query text it filters on the trigram
// and full text indexes and then ranks what survives. Without query text -
// mention autocomplete asking for suggestions, for example - there is nothing
// to rank: ts_rank_cd and similarity against an empty string are zero for every
// row, so the old shared statement paid for a to_tsvector and three similarity
// calls per row of the whole table to sort by nothing. Browse mode skips all of
// that and orders by the popularity tiebreak the ranked query fell through to.
func browsing(query searchservice.Query) bool { return query.Folded == "" }

const searchUserColumns = `u.id,u.username,u.display_name,u.email,u.bio,u.avatar_id,u.account_type,u.provider,u.roles,u.active,u.created_at,u.updated_at`

// searchUsers ranks people by exact handle, prefix, full text and trigram
// similarity, keeping the follower count as the recommendation tiebreak.
func (s *Server) searchUsers(ctx context.Context, query searchservice.Query, viewer string, recommended bool, limit, offset int) ([]map[string]any, error) {
	statement := `SELECT ` + searchUserColumns + `
		FROM users u
		LEFT JOIN (SELECT followee_id,count(*) AS followers FROM follows GROUP BY followee_id) fc ON fc.followee_id=u.id
		WHERE u.active AND u.id<>$1 AND ` + visibility.NotBlockedBetween("u.id", "$1") + `
		ORDER BY COALESCE(fc.followers,0) DESC,u.username
		LIMIT $2 OFFSET $3`
	args := []any{viewer, limit, offset}
	if !browsing(query) {
		statement = `SELECT ` + searchUserColumns + `
		FROM users u
		LEFT JOIN (SELECT followee_id,count(*) AS followers FROM follows GROUP BY followee_id) fc ON fc.followee_id=u.id
		WHERE u.active AND u.id<>$1 AND ` + visibility.NotBlockedBetween("u.id", "$1") + `
		AND (lower(u.username) LIKE $4 ESCAPE E'\\' OR lower(u.display_name) LIKE $4 ESCAPE E'\\' OR lower(u.bio) LIKE $4 ESCAPE E'\\' OR lower(u.username) % $3 OR lower(u.display_name) % $3 OR word_similarity($3,lower(u.bio)) >= 0.3 OR to_tsvector('simple',u.username||' '||u.display_name||' '||u.bio) @@ websearch_to_tsquery('simple',$2))
		ORDER BY (
			CASE WHEN lower(u.username)=$3 THEN 1000 ELSE 0 END +
			CASE WHEN lower(u.username) LIKE $3||'%' THEN 250 ELSE 0 END +
			ts_rank_cd(to_tsvector('simple',u.username||' '||u.display_name||' '||u.bio),websearch_to_tsquery('simple',$2))*100 +
			greatest(similarity(lower(u.username),$3)*80,similarity(lower(u.display_name),$3)*40,word_similarity($3,lower(u.bio))*25)
		) DESC, CASE WHEN $7 THEN COALESCE(fc.followers,0) ELSE 0 END DESC,u.username
		LIMIT $5 OFFSET $6`
		args = []any{viewer, query.Raw, query.Folded, query.Pattern, limit, offset, recommended}
	}
	rows, err := s.repo.Pool().Query(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		user, scanErr := scanUserRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, publicUserView(user))
	}
	return items, rows.Err()
}

func (s *Server) searchPosts(ctx context.Context, query searchservice.Query, viewer string, limit, offset int) ([]model.Moin, error) {
	where := visibility.PublishedAndVisible("p", "$1")
	args := []any{viewer}
	order := `p.published_at DESC,p.id DESC`
	if !browsing(query) {
		where = append(where, `(lower(p.content) LIKE $4 ESCAPE E'\\' OR lower(p.content) % $3 OR word_similarity($3,lower(p.content)) >= 0.3 OR to_tsvector('simple',p.content) @@ websearch_to_tsquery('simple',$2))`)
		args = append(args, query.Raw, query.Folded, query.Pattern)
		order = `(CASE WHEN lower(p.content)=$3 THEN 500 ELSE 0 END + ts_rank_cd(to_tsvector('simple',p.content),websearch_to_tsquery('simple',$2))*100 + greatest(similarity(lower(p.content),$3),word_similarity($3,lower(p.content)))*40) DESC,p.published_at DESC,p.id DESC`
	}
	posts, err := feedservice.QueryPosts(ctx, s.repo.Pool(), where, args, order, limit, offset, viewer)
	if err != nil {
		return nil, err
	}
	decorateRecommendations(posts)
	return posts, nil
}

const searchTopicColumns = `t.id,t.slug,t.name,t.description,t.created_at,
		(SELECT count(*) FROM user_topic_follows WHERE topic_id=t.id),
		(SELECT count(*) FROM post_topics pt JOIN posts p ON p.id=pt.post_id WHERE pt.topic_id=t.id AND p.status='published' AND p.visibility='public'),
		EXISTS(SELECT 1 FROM user_topic_follows WHERE topic_id=t.id AND user_id=$1)`

func (s *Server) searchTopics(ctx context.Context, query searchservice.Query, viewer string, limit, offset int) ([]model.Topic, error) {
	statement := `SELECT ` + searchTopicColumns + ` FROM topics t ORDER BY t.name LIMIT $2 OFFSET $3`
	args := []any{viewer, limit, offset}
	if !browsing(query) {
		statement = `SELECT ` + searchTopicColumns + ` FROM topics t
		WHERE lower(t.name) LIKE $4 ESCAPE E'\\' OR lower(t.slug) LIKE $4 ESCAPE E'\\' OR lower(t.name) % $3 OR lower(t.slug) % $3 OR word_similarity($3,lower(t.description)) >= 0.3 OR to_tsvector('simple',t.name||' '||t.description) @@ websearch_to_tsquery('simple',$2)
		ORDER BY (
			CASE WHEN lower(t.slug)=$3 OR lower(t.name)=$3 THEN 1000 ELSE 0 END +
			CASE WHEN lower(t.slug) LIKE $3||'%' OR lower(t.name) LIKE $3||'%' THEN 250 ELSE 0 END +
			ts_rank_cd(to_tsvector('simple',t.name||' '||t.description),websearch_to_tsquery('simple',$2))*100 +
			greatest(similarity(lower(t.slug),$3)*100,similarity(lower(t.name),$3)*80,word_similarity($3,lower(t.description))*30)
		) DESC,t.name LIMIT $5 OFFSET $6`
		args = []any{viewer, query.Raw, query.Folded, query.Pattern, limit, offset}
	}
	rows, err := s.repo.Pool().Query(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Topic, 0, limit)
	for rows.Next() {
		var topic model.Topic
		if err := rows.Scan(&topic.ID, &topic.Slug, &topic.Name, &topic.Description, &topic.CreatedAt, &topic.FollowerCount, &topic.MoinCount, &topic.Following); err != nil {
			return nil, err
		}
		items = append(items, topic)
	}
	return items, rows.Err()
}

const searchMoimColumns = `m.id,m.slug,m.name,m.description,m.owner_id,m.visibility,m.created_at,
		(SELECT count(*) FROM moim_members WHERE moim_id=m.id),(SELECT count(*) FROM posts WHERE moim_id=m.id AND status='published'),
		EXISTS(SELECT 1 FROM moim_members WHERE moim_id=m.id AND user_id=$1)`

// moimVisible keeps a private Moim out of search for everyone but its members.
const moimVisible = `(m.visibility='public' OR EXISTS(SELECT 1 FROM moim_members WHERE moim_id=m.id AND user_id=$1))`

func (s *Server) searchMoims(ctx context.Context, query searchservice.Query, viewer string, limit, offset int) ([]model.Moim, error) {
	statement := `SELECT ` + searchMoimColumns + ` FROM moims m WHERE ` + moimVisible + ` ORDER BY m.created_at DESC LIMIT $2 OFFSET $3`
	args := []any{viewer, limit, offset}
	if !browsing(query) {
		statement = `SELECT ` + searchMoimColumns + ` FROM moims m
		WHERE ` + moimVisible + `
		AND (lower(m.name) LIKE $4 ESCAPE E'\\' OR lower(m.slug) LIKE $4 ESCAPE E'\\' OR lower(m.description) LIKE $4 ESCAPE E'\\' OR lower(m.name) % $3 OR lower(m.slug) % $3 OR word_similarity($3,lower(m.description)) >= 0.3 OR to_tsvector('simple',m.name||' '||m.description) @@ websearch_to_tsquery('simple',$2))
		ORDER BY (
			CASE WHEN lower(m.slug)=$3 OR lower(m.name)=$3 THEN 1000 ELSE 0 END +
			CASE WHEN lower(m.slug) LIKE $3||'%' OR lower(m.name) LIKE $3||'%' THEN 250 ELSE 0 END +
			ts_rank_cd(to_tsvector('simple',m.name||' '||m.description),websearch_to_tsquery('simple',$2))*100 +
			greatest(similarity(lower(m.slug),$3)*100,similarity(lower(m.name),$3)*80,word_similarity($3,lower(m.description))*30)
		) DESC,m.created_at DESC LIMIT $5 OFFSET $6`
		args = []any{viewer, query.Raw, query.Folded, query.Pattern, limit, offset}
	}
	rows, err := s.repo.Pool().Query(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Moim, 0, limit)
	for rows.Next() {
		var item model.Moim
		if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.Description, &item.OwnerID, &item.Visibility, &item.CreatedAt, &item.MemberCount, &item.MoinCount, &item.Joined); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
