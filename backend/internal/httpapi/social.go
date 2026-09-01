package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	feedservice "github.com/hkjang/moina/backend/internal/feed"
	mediastore "github.com/hkjang/moina/backend/internal/media"
	"github.com/hkjang/moina/backend/internal/model"
	searchservice "github.com/hkjang/moina/backend/internal/search"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
)

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(chi.URLParam(r, "username"))
	var userID string
	if err := s.repo.Pool().QueryRow(r.Context(), `SELECT id FROM users WHERE lower(username)=lower($1) AND active`, username).Scan(&userID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "사용자를 찾을 수 없습니다")
		return
	}
	s.writeProfile(w, r, userID)
}

func (s *Server) writeProfile(w http.ResponseWriter, r *http.Request, userID string) {
	viewer := getPrincipal(r).User.ID
	user, err := scanUserRow(s.repo.Pool().QueryRow(r.Context(), `SELECT `+userSelectColumns+` FROM users WHERE id=$1 AND active`, userID))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "사용자를 찾을 수 없습니다")
		return
	}
	var followers, following, posts, signals int64
	var followed, blocked, muted bool
	err = s.repo.Pool().QueryRow(r.Context(), `SELECT
		(SELECT count(*) FROM follows WHERE followee_id=$1),
		(SELECT count(*) FROM follows WHERE follower_id=$1),
		(SELECT count(*) FROM posts WHERE author_id=$1 AND status='published' AND visibility='public'),
		(SELECT count(*) FROM reactions r JOIN posts p ON p.id=r.post_id WHERE p.author_id=$1 AND p.status='published' AND p.visibility='public'),
		EXISTS(SELECT 1 FROM follows WHERE follower_id=$2 AND followee_id=$1),
		EXISTS(SELECT 1 FROM blocks WHERE blocker_id=$2 AND blocked_id=$1),
		EXISTS(SELECT 1 FROM mutes WHERE muter_id=$2 AND muted_id=$1)`, userID, viewer).Scan(&followers, &following, &posts, &signals, &followed, &blocked, &muted)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "프로필을 불러올 수 없습니다")
		return
	}
	avatarURL := ""
	if user.AvatarID != "" {
		avatarURL = "/api/v1/media/" + user.AvatarID
	}
	view := map[string]any{"id": user.ID, "username": user.Username, "displayName": user.DisplayName, "bio": user.Bio, "avatarId": user.AvatarID, "avatarUrl": avatarURL, "accountType": user.AccountType, "followerCount": followers, "followingCount": following, "moinCount": posts, "signal": signals, "following": followed, "followed": followed, "blocked": blocked, "muted": muted, "createdAt": user.CreatedAt, "updatedAt": user.UpdatedAt}
	if viewer == userID {
		view["email"] = user.Email
		view["provider"] = user.Provider
		view["roles"] = user.Roles
	}
	writeData(w, http.StatusOK, view)
}

func (s *Server) followUser(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	targetID := strings.TrimSpace(chi.URLParam(r, "userID"))
	if targetID == p.User.ID {
		writeError(w, http.StatusBadRequest, "self_link", "자기 자신을 Link할 수 없습니다")
		return
	}
	var allowed bool
	err := s.repo.Pool().QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.active) AND NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$1 AND b.blocked_id=$2) OR (b.blocker_id=$2 AND b.blocked_id=$1))`, targetID, p.User.ID).Scan(&allowed)
	if err != nil || !allowed {
		writeError(w, http.StatusNotFound, "not_found", "Link할 사용자를 찾을 수 없습니다")
		return
	}
	tx, err := s.repo.Pool().Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Link를 저장할 수 없습니다")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), `INSERT INTO follows(follower_id,followee_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, p.User.ID, targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Link를 저장할 수 없습니다")
		return
	}
	if tag.RowsAffected() > 0 {
		if err := s.enqueueNotification(r.Context(), tx, targetID, p.User.ID, "follow", p.User.ID,
			map[string]string{"userId": p.User.ID}, "notification:follow:"+secure.NewID("op")); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Link를 저장할 수 없습니다")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Link를 저장할 수 없습니다")
		return
	}
	s.audit(r, "social.follow", "user", targetID, true, nil)
	writeData(w, http.StatusOK, map[string]bool{"following": true, "followed": true})
}

func (s *Server) unfollowUser(w http.ResponseWriter, r *http.Request) {
	_, err := s.repo.Pool().Exec(r.Context(), `DELETE FROM follows WHERE follower_id=$1 AND followee_id=$2`, getPrincipal(r).User.ID, chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Link를 해제할 수 없습니다")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) blockUser(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	target := chi.URLParam(r, "userID")
	if target == p.User.ID {
		writeError(w, http.StatusBadRequest, "self_block", "자기 자신을 차단할 수 없습니다")
		return
	}
	tx, err := s.repo.Pool().Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "사용자를 차단할 수 없습니다")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `INSERT INTO blocks(blocker_id,blocked_id) SELECT $1,id FROM users WHERE id=$2 AND active ON CONFLICT DO NOTHING`, p.User.ID, target); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "사용자를 차단할 수 없습니다")
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM follows WHERE (follower_id=$1 AND followee_id=$2) OR (follower_id=$2 AND followee_id=$1)`, p.User.ID, target); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "사용자를 차단할 수 없습니다")
		return
	}
	s.audit(r, "social.block", "user", target, true, nil)
	writeData(w, http.StatusOK, map[string]bool{"blocked": true})
}

func (s *Server) unblockUser(w http.ResponseWriter, r *http.Request) {
	_, err := s.repo.Pool().Exec(r.Context(), `DELETE FROM blocks WHERE blocker_id=$1 AND blocked_id=$2`, getPrincipal(r).User.ID, chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "차단을 해제할 수 없습니다")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) muteUser(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	target := chi.URLParam(r, "userID")
	if target == p.User.ID {
		writeError(w, http.StatusBadRequest, "self_mute", "자기 자신을 숨길 수 없습니다")
		return
	}
	_, err := s.repo.Pool().Exec(r.Context(), `INSERT INTO mutes(muter_id,muted_id) SELECT $1,id FROM users WHERE id=$2 AND active ON CONFLICT DO NOTHING`, p.User.ID, target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "사용자를 숨길 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, map[string]bool{"muted": true})
}

func (s *Server) unmuteUser(w http.ResponseWriter, r *http.Request) {
	_, err := s.repo.Pool().Exec(r.Context(), `DELETE FROM mutes WHERE muter_id=$1 AND muted_id=$2`, getPrincipal(r).User.ID, chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "숨김을 해제할 수 없습니다")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listTopics(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	viewer := getPrincipal(r).User.ID
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	pattern := "%" + escapeLike(strings.ToLower(query)) + "%"
	sort := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort")))
	if sort == "" {
		sort = "popular"
	}
	if !slicesContains([]string{"popular", "trending", "recent", "name"}, sort) {
		writeError(w, http.StatusBadRequest, "invalid_sort", "Topic 정렬 방식이 올바르지 않습니다")
		return
	}
	rows, err := s.repo.Pool().Query(r.Context(), `WITH post_activity AS (
		SELECT pt.topic_id,
			count(*)::bigint AS moin_count,
			count(*) FILTER (WHERE p.published_at >= statement_timestamp()-interval '24 hours')::bigint AS day_moins,
			count(*) FILTER (WHERE p.published_at >= statement_timestamp()-interval '7 days' AND p.published_at < statement_timestamp()-interval '24 hours')::bigint AS week_moins,
			max(p.published_at) AS latest_published_at
		FROM post_topics pt
		JOIN posts p ON p.id=pt.post_id
		WHERE p.status='published' AND p.visibility='public'
		GROUP BY pt.topic_id
	), reaction_activity AS (
		SELECT pt.topic_id,
			count(*) FILTER (WHERE r.created_at >= statement_timestamp()-interval '24 hours')::bigint AS day_signals,
			count(*) FILTER (WHERE r.created_at >= statement_timestamp()-interval '7 days' AND r.created_at < statement_timestamp()-interval '24 hours')::bigint AS week_signals
		FROM post_topics pt
		JOIN posts p ON p.id=pt.post_id AND p.status='published' AND p.visibility='public'
		JOIN reactions r ON r.post_id=p.id AND r.created_at >= statement_timestamp()-interval '7 days'
		GROUP BY pt.topic_id
	), topic_rows AS (
		SELECT t.id,t.slug,t.name,t.description,t.created_at,
			(SELECT count(*) FROM user_topic_follows utf WHERE utf.topic_id=t.id)::bigint AS follower_count,
			COALESCE(pa.moin_count,0)::bigint AS moin_count,
			EXISTS(SELECT 1 FROM user_topic_follows utf WHERE utf.topic_id=t.id AND utf.user_id=$1) AS following,
			(COALESCE(pa.day_moins,0)*8 + COALESCE(pa.week_moins,0)*2 + COALESCE(ra.day_signals,0)*2 + COALESCE(ra.week_signals,0)*0.5)::double precision AS trend_score,
			pa.latest_published_at
		FROM topics t
		LEFT JOIN post_activity pa ON pa.topic_id=t.id
		LEFT JOIN reaction_activity ra ON ra.topic_id=t.id
		WHERE $2='' OR lower(t.name) LIKE $3 ESCAPE E'\\' OR lower(t.slug) LIKE $3 ESCAPE E'\\'
	)
	SELECT id,slug,name,description,created_at,follower_count,moin_count,following,trend_score
	FROM topic_rows
	WHERE $6<>'trending' OR trend_score>0
	ORDER BY
		CASE WHEN $6='trending' THEN trend_score END DESC,
		CASE WHEN $6='recent' THEN latest_published_at END DESC NULLS LAST,
		CASE WHEN $6='name' THEN lower(name) END,
		CASE WHEN $6='popular' THEN moin_count END DESC,
		follower_count DESC,lower(name),id
	LIMIT $4 OFFSET $5`, viewer, query, pattern, limit, offset, sort)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Topic을 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]model.Topic, 0)
	for rows.Next() {
		var topic model.Topic
		if err := rows.Scan(&topic.ID, &topic.Slug, &topic.Name, &topic.Description, &topic.CreatedAt, &topic.FollowerCount, &topic.MoinCount, &topic.Following, &topic.TrendScore); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Topic을 불러올 수 없습니다")
			return
		}
		items = append(items, topic)
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (s *Server) getTopic(w http.ResponseWriter, r *http.Request) {
	slug := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "slug")))
	viewer := getPrincipal(r).User.ID
	var topic model.Topic
	err := s.repo.Pool().QueryRow(r.Context(), `SELECT t.id,t.slug,t.name,t.description,t.created_at,(SELECT count(*) FROM user_topic_follows WHERE topic_id=t.id),(SELECT count(*) FROM post_topics pt JOIN posts p ON p.id=pt.post_id WHERE pt.topic_id=t.id AND p.status='published' AND p.visibility='public'),EXISTS(SELECT 1 FROM user_topic_follows WHERE topic_id=t.id AND user_id=$2) FROM topics t WHERE t.slug=$1`, slug, viewer).Scan(&topic.ID, &topic.Slug, &topic.Name, &topic.Description, &topic.CreatedAt, &topic.FollowerCount, &topic.MoinCount, &topic.Following)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Topic을 찾을 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, topic)
}

func (s *Server) followTopic(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Weight int `json:"weight"`
	}
	if r.ContentLength > 0 && !decodeJSON(w, r, &input) {
		return
	}
	if input.Weight == 0 {
		input.Weight = 50
	}
	if input.Weight < 1 || input.Weight > 100 {
		writeError(w, http.StatusBadRequest, "invalid_weight", "Topic 가중치는 1~100이어야 합니다")
		return
	}
	tag, err := s.repo.Pool().Exec(r.Context(), `INSERT INTO user_topic_follows(user_id,topic_id,weight) SELECT $1,id,$3 FROM topics WHERE slug=$2 ON CONFLICT(user_id,topic_id) DO UPDATE SET weight=EXCLUDED.weight`, getPrincipal(r).User.ID, strings.ToLower(chi.URLParam(r, "slug")), input.Weight)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Topic을 찾을 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, map[string]any{"following": true, "weight": input.Weight})
}

func (s *Server) unfollowTopic(w http.ResponseWriter, r *http.Request) {
	_, err := s.repo.Pool().Exec(r.Context(), `DELETE FROM user_topic_follows WHERE user_id=$1 AND topic_id=(SELECT id FROM topics WHERE slug=$2)`, getPrincipal(r).User.ID, strings.ToLower(chi.URLParam(r, "slug")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Topic Link를 해제할 수 없습니다")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	recommended := r.URL.Query().Get("recommended") == "true"
	searchType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	if searchType == "" {
		searchType = "all"
	}
	if !slicesContains([]string{"all", "posts", "users", "topics", "moims"}, searchType) {
		writeError(w, http.StatusBadRequest, "invalid_type", "검색 대상이 올바르지 않습니다")
		return
	}
	query, err := searchservice.Parse(r.URL.Query().Get("q"), recommended)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", "검색어는 1~100자로 입력해 주세요")
		return
	}
	limit, _ := pagination(r)
	viewer := getPrincipal(r).User.ID
	users := make([]map[string]any, 0)
	if searchType == "all" || searchType == "users" {
		usersRows, err := s.repo.Pool().Query(r.Context(), `SELECT `+userSelectColumns+` FROM users
		WHERE active AND id<>$4
		AND NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$4 AND b.blocked_id=users.id) OR (b.blocker_id=users.id AND b.blocked_id=$4))
		AND ($6 OR lower(username) LIKE $3 ESCAPE E'\\' OR lower(display_name) LIKE $3 ESCAPE E'\\' OR lower(bio) LIKE $3 ESCAPE E'\\' OR lower(username) % $2 OR lower(display_name) % $2 OR word_similarity($2,lower(bio)) >= 0.3 OR to_tsvector('simple',username||' '||display_name||' '||bio) @@ websearch_to_tsquery('simple',$1))
		ORDER BY (
			CASE WHEN $2<>'' AND lower(username)=$2 THEN 1000 ELSE 0 END +
			CASE WHEN $2<>'' AND lower(username) LIKE $2||'%%' THEN 250 ELSE 0 END +
			ts_rank_cd(to_tsvector('simple',username||' '||display_name||' '||bio),websearch_to_tsquery('simple',$1))*100 +
			greatest(similarity(lower(username),$2)*80,similarity(lower(display_name),$2)*40,word_similarity($2,lower(bio))*25)
		) DESC, CASE WHEN $6 THEN (SELECT count(*) FROM follows rf WHERE rf.followee_id=users.id) ELSE 0 END DESC,username
		LIMIT $5`, query.Raw, query.Folded, query.Pattern, viewer, limit, recommended)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "검색할 수 없습니다")
			return
		}
		for usersRows.Next() {
			user, scanErr := scanUserRow(usersRows)
			if scanErr != nil {
				usersRows.Close()
				writeError(w, http.StatusInternalServerError, "storage_error", "검색할 수 없습니다")
				return
			}
			users = append(users, publicUserView(user))
		}
		usersRows.Close()
	}
	posts := make([]model.Moin, 0)
	if searchType == "all" || searchType == "posts" {
		postWhere := []string{
			"p.status='published'",
			`NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$1 AND b.blocked_id=p.author_id) OR (b.blocker_id=p.author_id AND b.blocked_id=$1))`,
			`(p.visibility='public' OR p.author_id=$1 OR p.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1 AND f.followee_id=p.author_id) OR p.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$1))`,
			`($5 OR lower(p.content) LIKE $4 ESCAPE E'\\' OR lower(p.content) % $3 OR word_similarity($3,lower(p.content)) >= 0.3 OR to_tsvector('simple',p.content) @@ websearch_to_tsquery('simple',$2))`,
		}
		postOrder := `(CASE WHEN $3<>'' AND lower(p.content)=$3 THEN 500 ELSE 0 END + ts_rank_cd(to_tsvector('simple',p.content),websearch_to_tsquery('simple',$2))*100 + greatest(similarity(lower(p.content),$3),word_similarity($3,lower(p.content)))*40) DESC,p.published_at DESC,p.id DESC`
		posts, err = feedservice.QueryPosts(r.Context(), s.repo.Pool(), postWhere, []any{viewer, query.Raw, query.Folded, query.Pattern, recommended}, postOrder, limit, 0, viewer)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "검색할 수 없습니다")
			return
		}
		decorateRecommendations(posts)
	}
	topics := make([]model.Topic, 0)
	if searchType == "all" || searchType == "topics" {
		topicRows, err := s.repo.Pool().Query(r.Context(), `SELECT t.id,t.slug,t.name,t.description,t.created_at,
		(SELECT count(*) FROM user_topic_follows WHERE topic_id=t.id),
		(SELECT count(*) FROM post_topics pt JOIN posts p ON p.id=pt.post_id WHERE pt.topic_id=t.id AND p.status='published' AND p.visibility='public'),
		EXISTS(SELECT 1 FROM user_topic_follows WHERE topic_id=t.id AND user_id=$4)
		FROM topics t
		WHERE $6 OR lower(t.name) LIKE $3 ESCAPE E'\\' OR lower(t.slug) LIKE $3 ESCAPE E'\\' OR lower(t.name) % $2 OR lower(t.slug) % $2 OR word_similarity($2,lower(t.description)) >= 0.3 OR to_tsvector('simple',t.name||' '||t.description) @@ websearch_to_tsquery('simple',$1)
		ORDER BY (
			CASE WHEN $2<>'' AND (lower(t.slug)=$2 OR lower(t.name)=$2) THEN 1000 ELSE 0 END +
			CASE WHEN $2<>'' AND (lower(t.slug) LIKE $2||'%%' OR lower(t.name) LIKE $2||'%%') THEN 250 ELSE 0 END +
			ts_rank_cd(to_tsvector('simple',t.name||' '||t.description),websearch_to_tsquery('simple',$1))*100 +
			greatest(similarity(lower(t.slug),$2)*100,similarity(lower(t.name),$2)*80,word_similarity($2,lower(t.description))*30)
		) DESC,t.name LIMIT $5`, query.Raw, query.Folded, query.Pattern, viewer, limit, recommended)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "검색할 수 없습니다")
			return
		}
		for topicRows.Next() {
			var topic model.Topic
			if err := topicRows.Scan(&topic.ID, &topic.Slug, &topic.Name, &topic.Description, &topic.CreatedAt, &topic.FollowerCount, &topic.MoinCount, &topic.Following); err != nil {
				topicRows.Close()
				writeError(w, http.StatusInternalServerError, "storage_error", "검색할 수 없습니다")
				return
			}
			topics = append(topics, topic)
		}
		topicRows.Close()
	}
	moims := make([]model.Moim, 0)
	if searchType == "all" || searchType == "moims" {
		moimRows, err := s.repo.Pool().Query(r.Context(), `SELECT m.id,m.slug,m.name,m.description,m.owner_id,m.visibility,m.created_at,
		(SELECT count(*) FROM moim_members WHERE moim_id=m.id),(SELECT count(*) FROM posts WHERE moim_id=m.id AND status='published'),
		EXISTS(SELECT 1 FROM moim_members WHERE moim_id=m.id AND user_id=$4)
		FROM moims m
		WHERE (m.visibility='public' OR EXISTS(SELECT 1 FROM moim_members WHERE moim_id=m.id AND user_id=$4))
		AND ($6 OR lower(m.name) LIKE $3 ESCAPE E'\\' OR lower(m.slug) LIKE $3 ESCAPE E'\\' OR lower(m.description) LIKE $3 ESCAPE E'\\' OR lower(m.name) % $2 OR lower(m.slug) % $2 OR word_similarity($2,lower(m.description)) >= 0.3 OR to_tsvector('simple',m.name||' '||m.description) @@ websearch_to_tsquery('simple',$1))
		ORDER BY (
			CASE WHEN $2<>'' AND (lower(m.slug)=$2 OR lower(m.name)=$2) THEN 1000 ELSE 0 END +
			CASE WHEN $2<>'' AND (lower(m.slug) LIKE $2||'%%' OR lower(m.name) LIKE $2||'%%') THEN 250 ELSE 0 END +
			ts_rank_cd(to_tsvector('simple',m.name||' '||m.description),websearch_to_tsquery('simple',$1))*100 +
			greatest(similarity(lower(m.slug),$2)*100,similarity(lower(m.name),$2)*80,word_similarity($2,lower(m.description))*30)
		) DESC,m.created_at DESC LIMIT $5`, query.Raw, query.Folded, query.Pattern, viewer, limit, recommended)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "검색할 수 없습니다")
			return
		}
		for moimRows.Next() {
			var item model.Moim
			if err := moimRows.Scan(&item.ID, &item.Slug, &item.Name, &item.Description, &item.OwnerID, &item.Visibility, &item.CreatedAt, &item.MemberCount, &item.MoinCount, &item.Joined); err != nil {
				moimRows.Close()
				writeError(w, http.StatusInternalServerError, "storage_error", "검색할 수 없습니다")
				return
			}
			moims = append(moims, item)
		}
		moimRows.Close()
	}
	writeData(w, http.StatusOK, map[string]any{"query": query.Raw, "users": users, "posts": posts, "topics": topics, "moims": moims})
}

func publicUserView(user model.User) map[string]any {
	avatarURL := ""
	if user.AvatarID != "" {
		avatarURL = "/api/v1/media/" + user.AvatarID
	}
	return map[string]any{"id": user.ID, "username": user.Username, "displayName": user.DisplayName, "bio": user.Bio, "avatarId": user.AvatarID, "avatarUrl": avatarURL, "accountType": user.AccountType, "createdAt": user.CreatedAt}
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	filter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("filter")))
	if filter == "" {
		filter = "all"
	}
	storedType := filter
	if filter == "signal" {
		storedType = "reaction"
	}
	if !slicesContains([]string{"all", "reaction", "mention"}, storedType) {
		writeError(w, http.StatusBadRequest, "invalid_filter", "알림 필터가 올바르지 않습니다")
		return
	}
	rows, err := s.repo.Pool().Query(r.Context(), `SELECT id,user_id,COALESCE(actor_id,''),type,target_id,payload,in_app,read_at,created_at FROM notifications WHERE user_id=$1 AND in_app AND ($4='all' OR type=$4) ORDER BY created_at DESC LIMIT $2 OFFSET $3`, getPrincipal(r).User.ID, limit, offset, storedType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "알림을 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]model.Notification, 0)
	for rows.Next() {
		var item model.Notification
		if err := rows.Scan(&item.ID, &item.UserID, &item.ActorID, &item.Type, &item.TargetID, &item.Payload, &item.InApp, &item.ReadAt, &item.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "알림을 불러올 수 없습니다")
			return
		}
		s.decorateNotification(r.Context(), &item)
		items = append(items, item)
	}
	var unread int64
	_ = s.repo.Pool().QueryRow(r.Context(), `SELECT count(*) FROM notifications WHERE user_id=$1 AND in_app AND read_at IS NULL`, getPrincipal(r).User.ID).Scan(&unread)
	writeData(w, http.StatusOK, map[string]any{"items": items, "unreadCount": unread, "limit": limit, "offset": offset})
}

func (s *Server) decorateNotification(ctx context.Context, item *model.Notification) {
	storedType := item.Type
	labels := map[string]string{
		"follow": "새로운 Link", "reaction": "새로운 Signal", "reply": "새로운 Echo", "mention": "새로운 멘션",
		"quote": "새로운 Quote Moin", "remoin": "새로운 Remoin",
		"approval_requested": "검토 요청", "approval_approved": "게시 승인", "approval_rejected": "게시 반려",
		"digest": "알림 브리핑",
	}
	item.Title = labels[storedType]
	if item.Title == "" {
		item.Title = "새 알림"
	}
	var payload map[string]any
	_ = json.Unmarshal(item.Payload, &payload)
	if value, ok := payload["body"].(string); ok {
		item.Body = value
	}
	if item.ActorID != "" {
		if actor, err := s.repo.UserByID(ctx, item.ActorID); err == nil {
			item.Actor = publicUserView(actor)
		}
	}
	switch storedType {
	case "follow":
		if actor, ok := item.Actor.(map[string]any); ok {
			if username, ok := actor["username"].(string); ok {
				item.TargetPath = "/profile/" + username
			}
		}
	case "reaction", "reply", "quote", "remoin", "mention", "approval_approved", "approval_rejected":
		postID := item.TargetID
		if value, ok := payload["postId"].(string); ok {
			postID = value
		}
		item.TargetPath = "/moin/" + postID
	case "approval_requested":
		item.TargetPath = "/admin/approvals"
	case "digest":
		item.TargetPath = "/notifications"
	}
	if storedType == "reaction" {
		item.Type = "signal"
	} else if storedType == "reply" {
		item.Type = "echo"
	}
}

func (s *Server) readNotifications(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.All && len(input.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids_required", "읽음 처리할 알림 ID 또는 all=true가 필요합니다")
		return
	}
	var err error
	if input.All {
		_, err = s.repo.Pool().Exec(r.Context(), `UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE user_id=$1 AND in_app`, getPrincipal(r).User.ID)
	} else {
		_, err = s.repo.Pool().Exec(r.Context(), `UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE user_id=$1 AND in_app AND id=ANY($2)`, getPrincipal(r).User.ID, input.IDs)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "알림을 읽음 처리할 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, map[string]bool{"updated": true})
}

var moimSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,49}$`)

func (s *Server) createMoim(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	var input struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		Visibility  string `json:"visibility"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name, input.Slug, input.Description, input.Visibility = strings.TrimSpace(input.Name), strings.ToLower(strings.TrimSpace(input.Slug)), strings.TrimSpace(input.Description), strings.ToLower(strings.TrimSpace(input.Visibility))
	if input.Visibility == "" {
		input.Visibility = "public"
	}
	if !validDisplayName(input.Name) || !moimSlugPattern.MatchString(input.Slug) || utf8.RuneCountInString(input.Description) > 1000 || !slicesContains([]string{"public", "private"}, input.Visibility) {
		writeError(w, http.StatusBadRequest, "invalid_moim", "Moim 이름, slug, 소개 또는 공개 범위가 올바르지 않습니다")
		return
	}
	moim := model.Moim{ID: secure.NewID("moim"), Slug: input.Slug, Name: input.Name, Description: input.Description, OwnerID: p.User.ID, Visibility: input.Visibility, MemberCount: 1, Joined: true, CreatedAt: time.Now().UTC()}
	tx, err := s.repo.Pool().Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Moim을 만들 수 없습니다")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `INSERT INTO moims(id,slug,name,description,owner_id,visibility,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, moim.ID, moim.Slug, moim.Name, moim.Description, moim.OwnerID, moim.Visibility, moim.CreatedAt)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO moim_members(moim_id,user_id,role) VALUES($1,$2,'owner')`, moim.ID, p.User.ID)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		if store.IsConflict(err) {
			writeError(w, http.StatusConflict, "slug_taken", "이미 사용 중인 Moim slug입니다")
		} else {
			writeError(w, http.StatusInternalServerError, "storage_error", "Moim을 만들 수 없습니다")
		}
		return
	}
	s.audit(r, "moim.create", "moim", moim.ID, true, nil)
	writeData(w, http.StatusCreated, moim)
}

func (s *Server) listMoims(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	viewer := getPrincipal(r).User.ID
	rows, err := s.repo.Pool().Query(r.Context(), `SELECT m.id,m.slug,m.name,m.description,m.owner_id,m.visibility,m.created_at,(SELECT count(*) FROM moim_members WHERE moim_id=m.id),(SELECT count(*) FROM posts WHERE moim_id=m.id AND status='published'),EXISTS(SELECT 1 FROM moim_members WHERE moim_id=m.id AND user_id=$1) FROM moims m WHERE m.visibility='public' OR EXISTS(SELECT 1 FROM moim_members WHERE moim_id=m.id AND user_id=$1) ORDER BY m.created_at DESC LIMIT $2 OFFSET $3`, viewer, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Moim 목록을 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]model.Moim, 0)
	for rows.Next() {
		var item model.Moim
		if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.Description, &item.OwnerID, &item.Visibility, &item.CreatedAt, &item.MemberCount, &item.MoinCount, &item.Joined); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Moim 목록을 불러올 수 없습니다")
			return
		}
		items = append(items, item)
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (s *Server) getMoim(w http.ResponseWriter, r *http.Request) {
	viewer := getPrincipal(r).User.ID
	slug := strings.ToLower(chi.URLParam(r, "slug"))
	var item model.Moim
	err := s.repo.Pool().QueryRow(r.Context(), `SELECT m.id,m.slug,m.name,m.description,m.owner_id,m.visibility,m.created_at,(SELECT count(*) FROM moim_members WHERE moim_id=m.id),(SELECT count(*) FROM posts WHERE moim_id=m.id AND status='published'),EXISTS(SELECT 1 FROM moim_members WHERE moim_id=m.id AND user_id=$2) FROM moims m WHERE m.slug=$1 AND (m.visibility='public' OR EXISTS(SELECT 1 FROM moim_members WHERE moim_id=m.id AND user_id=$2))`, slug, viewer).Scan(&item.ID, &item.Slug, &item.Name, &item.Description, &item.OwnerID, &item.Visibility, &item.CreatedAt, &item.MemberCount, &item.MoinCount, &item.Joined)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Moim을 찾을 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, item)
}

func (s *Server) joinMoim(w http.ResponseWriter, r *http.Request) {
	slug := strings.ToLower(chi.URLParam(r, "slug"))
	tag, err := s.repo.Pool().Exec(r.Context(), `INSERT INTO moim_members(moim_id,user_id,role) SELECT id,$2,'member' FROM moims WHERE slug=$1 AND visibility='public' ON CONFLICT DO NOTHING`, slug, getPrincipal(r).User.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "가입할 수 있는 공개 Moim을 찾을 수 없습니다")
		return
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		_ = s.repo.Pool().QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM moims m JOIN moim_members mm ON mm.moim_id=m.id WHERE m.slug=$1 AND mm.user_id=$2)`, slug, getPrincipal(r).User.ID).Scan(&exists)
		if !exists {
			writeError(w, http.StatusNotFound, "not_found", "가입할 수 있는 공개 Moim을 찾을 수 없습니다")
			return
		}
	}
	writeData(w, http.StatusOK, map[string]bool{"joined": true})
}

func (s *Server) leaveMoim(w http.ResponseWriter, r *http.Request) {
	tag, err := s.repo.Pool().Exec(r.Context(), `DELETE FROM moim_members mm USING moims m WHERE mm.moim_id=m.id AND m.slug=$1 AND mm.user_id=$2 AND mm.role<>'owner'`, strings.ToLower(chi.URLParam(r, "slug")), getPrincipal(r).User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Moim에서 나갈 수 없습니다")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "owner_cannot_leave", "Moim 소유자는 나갈 수 없습니다")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) uploadMedia(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.mediaSettings(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "미디어 설정을 확인할 수 없습니다")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxUploadBytes+(1<<20))
	// Keep only a small prefix in memory. net/http transparently spills larger
	// file parts to a temporary file, which is then streamed into PostgreSQL.
	if err := r.ParseMultipartForm(64 << 10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_media", "업로드 파일을 읽을 수 없습니다")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	altText := strings.TrimSpace(r.FormValue("altText"))
	if altText == "" {
		altText = strings.TrimSpace(r.FormValue("alt"))
	}
	if !utf8.ValidString(altText) || utf8.RuneCountInString(altText) > 500 || strings.ContainsRune(altText, '\x00') {
		writeError(w, http.StatusBadRequest, "invalid_alt_text", "대체 텍스트는 500자 이하로 입력해 주세요")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file_required", "file 필드가 필요합니다")
		return
	}
	defer file.Close()
	if header.Size <= 0 || header.Size > cfg.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "media_too_large", "파일이 비어 있거나 업로드 한도를 넘었습니다")
		return
	}
	sniff := make([]byte, min(header.Size, int64(4096)))
	n, readErr := io.ReadFull(file, sniff)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		writeError(w, http.StatusBadRequest, "invalid_media", "업로드 파일을 읽을 수 없습니다")
		return
	}
	sniff = sniff[:n]
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_media", "업로드 파일을 읽을 수 없습니다")
		return
	}
	mimeType := detectMediaType(sniff)
	if !slicesContains([]string{"image/jpeg", "image/png", "image/gif", "image/webp", "video/mp4", "video/webm"}, mimeType) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media", "JPEG, PNG, GIF, WebP 이미지 또는 MP4, WebM 동영상만 업로드할 수 있습니다")
		return
	}
	width, height := imageDimensionsFrom(file)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_media", "업로드 파일을 읽을 수 없습니다")
		return
	}
	mediaType := "image"
	if strings.HasPrefix(mimeType, "video/") {
		mediaType = "video"
	}
	media := model.Media{ID: secure.NewID("media"), OwnerID: getPrincipal(r).User.ID, Filename: safeFilename(header), AltText: altText, MIMEType: mimeType, Type: mediaType, Size: header.Size, Width: width, Height: height, CreatedAt: time.Now().UTC()}
	media.URL = "/api/v1/media/" + media.ID
	_, err = s.media.Put(r.Context(), mediastore.PutObject{Metadata: mediastore.Metadata{
		ID: media.ID, OwnerID: media.OwnerID, Filename: media.Filename, AltText: media.AltText, MIMEType: media.MIMEType,
		Size: media.Size, Width: media.Width, Height: media.Height, CreatedAt: media.CreatedAt,
	}, Body: file})
	if err != nil {
		if errors.Is(err, mediastore.ErrUploadBusy) {
			writeError(w, http.StatusTooManyRequests, "media_upload_busy", "같은 사용자의 다른 미디어 업로드가 진행 중입니다. 잠시 후 다시 시도해 주세요")
			return
		}
		if errors.Is(err, mediastore.ErrQuotaExceeded) {
			writeError(w, http.StatusTooManyRequests, "media_quota_exceeded", "게시물에 연결하지 않은 미디어가 너무 많습니다. 기존 업로드를 게시물에 첨부하거나 정리 후 다시 시도해 주세요")
			return
		}
		if errors.Is(err, mediastore.ErrTooLarge) || errors.Is(err, mediastore.ErrSizeMismatch) {
			writeError(w, http.StatusRequestEntityTooLarge, "media_too_large", "파일이 비어 있거나 업로드 한도를 넘었습니다")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error", "미디어를 저장할 수 없습니다")
		return
	}
	s.audit(r, "media.upload", "media", media.ID, true, map[string]any{"mimeType": media.MIMEType, "size": media.Size})
	writeData(w, http.StatusCreated, media)
}

func detectMediaType(data []byte) string {
	detected := http.DetectContentType(data)
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		return "video/mp4"
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) && bytes.Contains(bytes.ToLower(data[:min(len(data), 4096)]), []byte("webm")) {
		return "video/webm"
	}
	return detected
}

func safeFilename(header *multipart.FileHeader) string {
	name := filepath.Base(strings.TrimSpace(header.Filename))
	name = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) || character == '/' || character == '\\' {
			return -1
		}
		return character
	}, name)
	if name == "" {
		return "image"
	}
	if len([]rune(name)) > 200 {
		name = string([]rune(name)[:200])
	}
	return name
}

func imageDimensionsFrom(file multipart.File) (int, int) {
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0
	}
	return config.Width, config.Height
}

func (s *Server) getMedia(w http.ResponseWriter, r *http.Request) {
	principal := getPrincipal(r)
	id := chi.URLParam(r, "mediaID")
	if !hasPermission(principal.Permissions, "posts:read") {
		if principal.APIKey {
			writeError(w, http.StatusForbidden, "forbidden", "이 작업을 수행할 권한이 없습니다")
			return
		}
		var owned bool
		if err := s.repo.Pool().QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM media_assets WHERE id=$1 AND owner_id=$2)`, id, principal.User.ID).Scan(&owned); err != nil || !owned {
			writeError(w, http.StatusForbidden, "forbidden", "이 작업을 수행할 권한이 없습니다")
			return
		}
	}
	object, err := s.media.Open(r.Context(), id, principal.User.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "미디어를 찾을 수 없습니다")
		return
	}
	defer object.Body.Close()
	w.Header().Set("Content-Type", object.Metadata.MIMEType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", strings.ReplaceAll(object.Metadata.Filename, `"`, "")))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, object.Metadata.Filename, object.Metadata.CreatedAt, object.Body)
}

func (s *Server) deleteMedia(w http.ResponseWriter, r *http.Request) {
	mediaID := strings.TrimSpace(chi.URLParam(r, "mediaID"))
	ownerID := getPrincipal(r).User.ID
	tag, err := s.repo.Pool().Exec(r.Context(), `DELETE FROM media_assets asset
		WHERE asset.id=$1 AND asset.owner_id=$2
		AND NOT EXISTS(SELECT 1 FROM post_media linked WHERE linked.media_id=asset.id)
		AND NOT EXISTS(SELECT 1 FROM users avatar_user WHERE avatar_user.avatar_id=asset.id)`, mediaID, ownerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "미디어를 삭제할 수 없습니다")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "media_in_use_or_unavailable", "사용 중이거나 삭제할 수 없는 미디어입니다")
		return
	}
	s.audit(r, "media.delete", "media", mediaID, true, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createReport(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	var input struct {
		TargetType string `json:"targetType"`
		TargetID   string `json:"targetId"`
		Reason     string `json:"reason"`
		Detail     string `json:"detail"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.TargetType, input.TargetID, input.Reason, input.Detail = strings.ToLower(strings.TrimSpace(input.TargetType)), strings.TrimSpace(input.TargetID), strings.TrimSpace(input.Reason), strings.TrimSpace(input.Detail)
	if !slicesContains([]string{"post", "user", "moim"}, input.TargetType) || input.TargetID == "" || input.Reason == "" || utf8.RuneCountInString(input.Reason) > 120 || utf8.RuneCountInString(input.Detail) > 2000 {
		writeError(w, http.StatusBadRequest, "invalid_report", "신고 대상, 사유 또는 상세 내용이 올바르지 않습니다")
		return
	}
	var exists bool
	switch input.TargetType {
	case "post":
		_ = s.repo.Pool().QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM posts WHERE id=$1 AND status<>'deleted')`, input.TargetID).Scan(&exists)
	case "user":
		_ = s.repo.Pool().QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND active)`, input.TargetID).Scan(&exists)
	case "moim":
		_ = s.repo.Pool().QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM moims WHERE id=$1)`, input.TargetID).Scan(&exists)
	}
	if !exists {
		writeError(w, http.StatusNotFound, "target_not_found", "신고 대상을 찾을 수 없습니다")
		return
	}
	report := model.Report{ID: secure.NewID("report"), ReporterID: p.User.ID, TargetType: input.TargetType, TargetID: input.TargetID, Reason: input.Reason, Detail: input.Detail, Status: "open", CreatedAt: time.Now().UTC()}
	_, err := s.repo.Pool().Exec(r.Context(), `INSERT INTO reports(id,reporter_id,target_type,target_id,reason,detail,status,created_at) VALUES($1,$2,$3,$4,$5,$6,'open',$7)`, report.ID, report.ReporterID, report.TargetType, report.TargetID, report.Reason, report.Detail, report.CreatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "신고를 접수할 수 없습니다")
		return
	}
	s.audit(r, "report.create", report.TargetType, report.TargetID, true, map[string]string{"reportId": report.ID, "reason": report.Reason})
	writeData(w, http.StatusCreated, report)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}
