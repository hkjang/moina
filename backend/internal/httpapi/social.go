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
	limit, offset, ok := pagination(w, r)
	if !ok {
		return
	}
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
	// offset has always been part of the documented contract; it used to be
	// parsed and dropped, capping every search at a single page.
	limit, offset, ok := pagination(w, r)
	if !ok {
		return
	}
	viewer := getPrincipal(r).User.ID
	wants := func(kind string) bool { return searchType == "all" || searchType == kind }

	users := make([]map[string]any, 0)
	posts := make([]model.Moin, 0)
	topics := make([]model.Topic, 0)
	moims := make([]model.Moim, 0)
	lookups := make([]func(context.Context) error, 0, 4)
	if wants("users") {
		lookups = append(lookups, func(ctx context.Context) error {
			found, err := s.searchUsers(ctx, query, viewer, recommended, limit, offset)
			users = found
			return err
		})
	}
	if wants("posts") {
		lookups = append(lookups, func(ctx context.Context) error {
			found, err := s.searchPosts(ctx, query, viewer, limit, offset)
			posts = found
			return err
		})
	}
	if wants("topics") {
		lookups = append(lookups, func(ctx context.Context) error {
			found, err := s.searchTopics(ctx, query, viewer, limit, offset)
			topics = found
			return err
		})
	}
	if wants("moims") {
		lookups = append(lookups, func(ctx context.Context) error {
			found, err := s.searchMoims(ctx, query, viewer, limit, offset)
			moims = found
			return err
		})
	}
	// Each lookup writes its own result variable and reads none of the others,
	// so running them together needs no further synchronisation.
	if err := runSearches(r.Context(), lookups); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "검색할 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, map[string]any{"query": query.Raw, "limit": limit, "offset": offset, "users": users, "posts": posts, "topics": topics, "moims": moims})
}

func publicUserView(user model.User) map[string]any {
	avatarURL := ""
	if user.AvatarID != "" {
		avatarURL = "/api/v1/media/" + user.AvatarID
	}
	return map[string]any{"id": user.ID, "username": user.Username, "displayName": user.DisplayName, "bio": user.Bio, "avatarId": user.AvatarID, "avatarUrl": avatarURL, "accountType": user.AccountType, "createdAt": user.CreatedAt}
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := pagination(w, r)
	if !ok {
		return
	}
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
	limit, offset, ok := pagination(w, r)
	if !ok {
		return
	}
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
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media", unsupportedMediaMessage(sniff))
		return
	}
	width, height := imageDimensionsFrom(file, sniff)
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

// mp4MajorBrands는 ftyp box의 major brand 중 MP4 동영상 컨테이너를 뜻하는 값입니다.
// HEIC·AVIF 사진, M4A 오디오, QuickTime 동영상도 같은 ISO Base Media 컨테이너라
// 똑같이 "ftyp"로 시작하므로 brand로만 MP4와 구분할 수 있습니다.
var mp4MajorBrands = map[string]bool{
	"avc1": true, "cmfc": true, "dash": true, "iso2": true, "iso4": true, "iso5": true,
	"iso6": true, "isom": true, "mmp4": true, "mp41": true, "mp42": true, "mp71": true,
	"msnv": true, "M4V ": true, "M4VH": true, "M4VP": true,
}

func detectMediaType(data []byte) string {
	detected := http.DetectContentType(data)
	// "ftyp" 시그니처만 보면 아이폰이 찍은 HEIC 사진까지 MP4로 저장돼 재생할 수 없는
	// 동영상 첨부가 되므로, 이어지는 major brand까지 확인해 실제 MP4만 통과시킵니다.
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		if mp4MajorBrands[string(data[8:12])] {
			return "video/mp4"
		}
		// M4A 오디오처럼 호환 brand에 "mp4"를 함께 적어 두는 형식이 많아
		// http.DetectContentType도 MP4로 답하므로, major brand가 아니라고 판정한 뒤에는
		// 표준 감지 결과로 되돌아가지 않고 지원하지 않는 형식으로 남깁니다.
		return "application/octet-stream"
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) && bytes.Contains(bytes.ToLower(data[:min(len(data), 4096)]), []byte("webm")) {
		return "video/webm"
	}
	return detected
}

// heifMajorBrands는 ftyp box의 major brand 중 HEIC·HEIF 사진을 뜻하는 값입니다.
// 아이폰이 찍은 사진은 "heic"·"heix"를, HEIF 일반 형식은 "mif1"·"msf1"을 씁니다.
var heifMajorBrands = map[string]bool{
	"heic": true, "heim": true, "heis": true, "heix": true,
	"hevc": true, "hevm": true, "hevs": true, "hevx": true,
	"mif1": true, "msf1": true,
}

// heicUploadGuidance는 웹 앱의 HEIC 안내(frontend/src/utils/media.ts)와 같은 내용입니다.
const heicUploadGuidance = "HEIC/HEIF 사진은 아직 업로드할 수 없습니다. iPhone은 설정 > 카메라 > 포맷에서 ‘높은 호환성’을 선택하면 앞으로 찍는 사진이 JPEG으로 저장되고, 이미 찍은 사진은 JPEG으로 내보낸 뒤 올려 주세요"

const supportedMediaFormats = "JPEG, PNG, GIF, WebP 이미지 또는 MP4, WebM 동영상만 업로드할 수 있습니다"

// unsupportedMediaMessage는 415로 거절한 업로드의 안내 문구를 고릅니다.
// HEIC은 아이폰의 기본 촬영 형식이라 지원 형식 목록만 돌려주면 갤러리에서 평범해
// 보이는 사진이 왜 거절됐는지 알 수 없으므로, 다시 시도할 방법을 함께 알려 줍니다.
func unsupportedMediaMessage(data []byte) string {
	if len(data) >= 12 && string(data[4:8]) == "ftyp" && heifMajorBrands[string(data[8:12])] {
		return heicUploadGuidance
	}
	return supportedMediaFormats
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

// attrChars는 RFC 8187 ext-value에서 percent-encoding 없이 쓸 수 있는 문자입니다.
const attrChars = "!#$&+-.^_`|~"

// contentDisposition은 미디어 응답의 Content-Disposition 값을 만듭니다.
// 한글처럼 ASCII 밖 문자가 들어간 파일 이름을 header에 그대로 넣으면 브라우저마다
// 다른 문자 집합으로 읽어 저장 이름이 깨지므로, RFC 6266대로 ASCII fallback과
// UTF-8 filename*을 함께 내려보내 filename*을 읽는 브라우저가 원래 이름을 쓰게 합니다.
func contentDisposition(filename string) string {
	ascii := asciiFilename(filename)
	value := fmt.Sprintf("inline; filename=%q", ascii)
	if filename == "" || ascii == filename {
		return value
	}
	var encoded strings.Builder
	for _, character := range []byte(filename) {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' || strings.IndexByte(attrChars, character) >= 0 {
			encoded.WriteByte(character)
			continue
		}
		fmt.Fprintf(&encoded, "%%%02X", character)
	}
	return value + "; filename*=UTF-8''" + encoded.String()
}

// asciiFilename은 filename*을 읽지 못하는 브라우저가 쓸 fallback 이름을 만듭니다.
// header 값과 quoted-string을 깨뜨릴 수 있는 문자는 모두 밑줄로 바꿉니다.
func asciiFilename(filename string) string {
	var fallback strings.Builder
	for _, character := range filename {
		if character < 0x20 || character > 0x7e || character == '"' || character == '\\' {
			fallback.WriteRune('_')
			continue
		}
		fallback.WriteRune(character)
	}
	if strings.Trim(fallback.String(), "_ ") == "" {
		return "media"
	}
	return fallback.String()
}

func imageDimensionsFrom(file multipart.File, sniff []byte) (int, int) {
	// 표준 라이브러리에는 WebP 디코더가 없어 image.DecodeConfig이 실패하므로
	// 지원 형식인 WebP는 헤더에서 직접 읽습니다.
	if width, height, ok := webpDimensions(sniff); ok {
		return width, height
	}
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0
	}
	return config.Width, config.Height
}

// webpDimensions는 RIFF 컨테이너의 첫 chunk에서 WebP 이미지 크기를 읽습니다.
// image.DecodeConfig에 맡기면 WebP만 항상 0,0으로 저장돼 API client는 그림이
// 도착하기 전 자리를 잡을 수 없고 media 응답도 크기를 알려 주지 못합니다.
// chunk 종류는 세 가지입니다: VP8 (lossy), VP8L (lossless), VP8X (알파·애니메이션).
func webpDimensions(data []byte) (int, int, bool) {
	if len(data) < 20 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	payload := data[20:]
	var width, height int
	switch string(data[12:16]) {
	case "VP8X":
		// flags 1바이트와 reserved 3바이트 뒤에 canvas 크기가 24비트 little-endian으로
		// 1을 뺀 값으로 들어 있습니다. 애니메이션은 이 canvas 크기가 전체 크기입니다.
		if len(payload) < 10 {
			return 0, 0, false
		}
		width = (int(payload[4]) | int(payload[5])<<8 | int(payload[6])<<16) + 1
		height = (int(payload[7]) | int(payload[8])<<8 | int(payload[9])<<16) + 1
	case "VP8L":
		// 서명 1바이트 뒤에 14비트 너비와 14비트 높이가 1을 뺀 값으로 이어집니다.
		if len(payload) < 5 || payload[0] != 0x2f {
			return 0, 0, false
		}
		bits := uint32(payload[1]) | uint32(payload[2])<<8 | uint32(payload[3])<<16 | uint32(payload[4])<<24
		width = int(bits&0x3fff) + 1
		height = int((bits>>14)&0x3fff) + 1
	case "VP8 ":
		// 크기는 key frame만 실어 오므로 frame tag의 frame type 비트와 sync code를
		// 확인한 뒤 14비트 너비·높이를 읽습니다(남은 2비트는 표시 배율).
		if len(payload) < 10 || payload[0]&1 != 0 || payload[3] != 0x9d || payload[4] != 0x01 || payload[5] != 0x2a {
			return 0, 0, false
		}
		width = (int(payload[6]) | int(payload[7])<<8) & 0x3fff
		height = (int(payload[8]) | int(payload[9])<<8) & 0x3fff
	default:
		return 0, 0, false
	}
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
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
	w.Header().Set("Content-Disposition", contentDisposition(object.Metadata.Filename))
	// 본문은 media ID마다 고정이지만 볼 수 있는 사람은 차단과 공개 범위 변경으로 언제든 바뀌므로,
	// max-age로 캐시를 허용하면 게시물을 비공개로 돌리거나 차단한 뒤에도 이미 받아 간 브라우저가
	// 그 시간 동안 캐시에서 계속 볼 수 있습니다. no-cache로 매 요청 서버 검사를 거치게 하고
	// 본문 재전송만 ETag 재검증으로 줄입니다.
	w.Header().Set("Cache-Control", "private, no-cache")
	if etag := mediaETag(object.Metadata.SHA256); etag != "" {
		w.Header().Set("ETag", etag)
	}
	http.ServeContent(w, r, object.Metadata.Filename, object.Metadata.CreatedAt, object.Body)
}

// mediaETag는 저장할 때 계산한 SHA-256으로 강한 ETag를 만듭니다.
// 같은 media ID의 본문은 절대 바뀌지 않으므로 재검증 요청은 304로 끝나고,
// 값이 비었거나 hex가 아닌 예전 행은 ETag 없이 Last-Modified 재검증만 씁니다.
func mediaETag(sha256 string) string {
	if len(sha256) != 64 {
		return ""
	}
	for _, character := range sha256 {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return ""
		}
	}
	return `"` + sha256 + `"`
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

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}
