package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"github.com/hkjang/moina/backend/internal/model"
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
	var followers, following, posts int64
	var followed, blocked, muted bool
	err = s.repo.Pool().QueryRow(r.Context(), `SELECT
		(SELECT count(*) FROM follows WHERE followee_id=$1),
		(SELECT count(*) FROM follows WHERE follower_id=$1),
		(SELECT count(*) FROM posts WHERE author_id=$1 AND status='published'),
		EXISTS(SELECT 1 FROM follows WHERE follower_id=$2 AND followee_id=$1),
		EXISTS(SELECT 1 FROM blocks WHERE blocker_id=$2 AND blocked_id=$1),
		EXISTS(SELECT 1 FROM mutes WHERE muter_id=$2 AND muted_id=$1)`, userID, viewer).Scan(&followers, &following, &posts, &followed, &blocked, &muted)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "프로필을 불러올 수 없습니다")
		return
	}
	avatarURL := ""
	if user.AvatarID != "" {
		avatarURL = "/api/v1/media/" + user.AvatarID
	}
	view := map[string]any{"id": user.ID, "username": user.Username, "displayName": user.DisplayName, "bio": user.Bio, "avatarId": user.AvatarID, "avatarUrl": avatarURL, "accountType": user.AccountType, "followerCount": followers, "followingCount": following, "moinCount": posts, "following": followed, "followed": followed, "blocked": blocked, "muted": muted, "createdAt": user.CreatedAt, "updatedAt": user.UpdatedAt}
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
	tag, err := s.repo.Pool().Exec(r.Context(), `INSERT INTO follows(follower_id,followee_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, p.User.ID, targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Link를 저장할 수 없습니다")
		return
	}
	if tag.RowsAffected() > 0 {
		s.notify(r.Context(), targetID, p.User.ID, "follow", p.User.ID, map[string]string{"userId": p.User.ID})
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
	rows, err := s.repo.Pool().Query(r.Context(), `SELECT t.id,t.slug,t.name,t.description,t.created_at,(SELECT count(*) FROM user_topic_follows WHERE topic_id=t.id),(SELECT count(*) FROM post_topics pt JOIN posts p ON p.id=pt.post_id WHERE pt.topic_id=t.id AND p.status='published'),EXISTS(SELECT 1 FROM user_topic_follows WHERE topic_id=t.id AND user_id=$1) FROM topics t WHERE $2='' OR lower(t.name) LIKE $3 ESCAPE E'\\' OR lower(t.slug) LIKE $3 ESCAPE E'\\' ORDER BY (SELECT count(*) FROM post_topics WHERE topic_id=t.id) DESC,t.name LIMIT $4 OFFSET $5`, viewer, query, pattern, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Topic을 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]model.Topic, 0)
	for rows.Next() {
		var topic model.Topic
		if err := rows.Scan(&topic.ID, &topic.Slug, &topic.Name, &topic.Description, &topic.CreatedAt, &topic.FollowerCount, &topic.MoinCount, &topic.Following); err != nil {
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
	err := s.repo.Pool().QueryRow(r.Context(), `SELECT t.id,t.slug,t.name,t.description,t.created_at,(SELECT count(*) FROM user_topic_follows WHERE topic_id=t.id),(SELECT count(*) FROM post_topics pt JOIN posts p ON p.id=pt.post_id WHERE pt.topic_id=t.id AND p.status='published'),EXISTS(SELECT 1 FROM user_topic_follows WHERE topic_id=t.id AND user_id=$2) FROM topics t WHERE t.slug=$1`, slug, viewer).Scan(&topic.ID, &topic.Slug, &topic.Name, &topic.Description, &topic.CreatedAt, &topic.FollowerCount, &topic.MoinCount, &topic.Following)
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
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	recommended := r.URL.Query().Get("recommended") == "true"
	if !utf8.ValidString(query) || (!recommended && utf8.RuneCountInString(query) < 1) || utf8.RuneCountInString(query) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_query", "검색어는 1~100자로 입력해 주세요")
		return
	}
	limit, _ := pagination(r)
	pattern := "%" + escapeLike(strings.ToLower(query)) + "%"
	viewer := getPrincipal(r).User.ID
	usersRows, err := s.repo.Pool().Query(r.Context(), `SELECT `+userSelectColumns+` FROM users WHERE active AND id<>$2 AND (lower(username) LIKE $1 ESCAPE E'\\' OR lower(display_name) LIKE $1 ESCAPE E'\\' OR lower(bio) LIKE $1 ESCAPE E'\\') AND NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$2 AND b.blocked_id=users.id) OR (b.blocker_id=users.id AND b.blocked_id=$2)) ORDER BY CASE WHEN $4 THEN (SELECT count(*) FROM follows f WHERE f.followee_id=users.id) END DESC,username LIMIT $3`, pattern, viewer, limit, recommended)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "검색할 수 없습니다")
		return
	}
	users := make([]map[string]any, 0)
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
	postRows, err := s.repo.Pool().Query(r.Context(), `SELECT p.id FROM posts p WHERE p.status='published' AND lower(p.content) LIKE $1 ESCAPE E'\\' AND NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$2 AND b.blocked_id=p.author_id) OR (b.blocker_id=p.author_id AND b.blocked_id=$2)) AND (p.visibility='public' OR p.author_id=$2 OR p.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$2 AND f.followee_id=p.author_id) OR p.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$2)) ORDER BY p.created_at DESC LIMIT $3`, pattern, viewer, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "검색할 수 없습니다")
		return
	}
	posts := make([]model.Moin, 0)
	for postRows.Next() {
		var id string
		if postRows.Scan(&id) == nil {
			if post, loadErr := s.loadMoin(r.Context(), id, viewer); loadErr == nil {
				posts = append(posts, post)
			}
		}
	}
	postRows.Close()
	topicRows, _ := s.repo.Pool().Query(r.Context(), `SELECT id,slug,name,description,created_at FROM topics WHERE lower(name) LIKE $1 ESCAPE E'\\' OR lower(slug) LIKE $1 ESCAPE E'\\' ORDER BY name LIMIT $2`, pattern, limit)
	topics := make([]model.Topic, 0)
	if topicRows != nil {
		for topicRows.Next() {
			var topic model.Topic
			if topicRows.Scan(&topic.ID, &topic.Slug, &topic.Name, &topic.Description, &topic.CreatedAt) == nil {
				topics = append(topics, topic)
			}
		}
		topicRows.Close()
	}
	moims := make([]model.Moim, 0)
	moimRows, _ := s.repo.Pool().Query(r.Context(), `SELECT m.id,m.slug,m.name,m.description,m.owner_id,m.visibility,m.created_at,(SELECT count(*) FROM moim_members WHERE moim_id=m.id),(SELECT count(*) FROM posts WHERE moim_id=m.id AND status='published'),EXISTS(SELECT 1 FROM moim_members WHERE moim_id=m.id AND user_id=$2) FROM moims m WHERE (m.visibility='public' OR EXISTS(SELECT 1 FROM moim_members WHERE moim_id=m.id AND user_id=$2)) AND (lower(m.name) LIKE $1 ESCAPE E'\\' OR lower(m.slug) LIKE $1 ESCAPE E'\\' OR lower(m.description) LIKE $1 ESCAPE E'\\') ORDER BY m.created_at DESC LIMIT $3`, pattern, viewer, limit)
	if moimRows != nil {
		for moimRows.Next() {
			var item model.Moim
			if moimRows.Scan(&item.ID, &item.Slug, &item.Name, &item.Description, &item.OwnerID, &item.Visibility, &item.CreatedAt, &item.MemberCount, &item.MoinCount, &item.Joined) == nil {
				moims = append(moims, item)
			}
		}
		moimRows.Close()
	}
	writeData(w, http.StatusOK, map[string]any{"query": query, "users": users, "posts": posts, "topics": topics, "moims": moims})
}

func publicUserView(user model.User) map[string]any {
	avatarURL := ""
	if user.AvatarID != "" {
		avatarURL = "/api/v1/media/" + user.AvatarID
	}
	return map[string]any{"id": user.ID, "username": user.Username, "displayName": user.DisplayName, "bio": user.Bio, "avatarId": user.AvatarID, "avatarUrl": avatarURL, "accountType": user.AccountType, "createdAt": user.CreatedAt}
}

func (s *Server) notify(ctx context.Context, userID, actorID, kind, targetID string, payload any) {
	if userID == "" || userID == actorID {
		return
	}
	raw, _ := json.Marshal(payload)
	notification := model.Notification{ID: secure.NewID("noti"), UserID: userID, ActorID: actorID, Type: kind, TargetID: targetID, Payload: raw, CreatedAt: time.Now().UTC()}
	_, err := s.repo.Pool().Exec(ctx, `INSERT INTO notifications(id,user_id,actor_id,type,target_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, notification.ID, notification.UserID, nullableString(notification.ActorID), notification.Type, notification.TargetID, notification.Payload, notification.CreatedAt)
	if err == nil {
		s.decorateNotification(ctx, &notification)
		s.hub.publish(userID, notification)
	}
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
	rows, err := s.repo.Pool().Query(r.Context(), `SELECT id,user_id,COALESCE(actor_id,''),type,target_id,payload,read_at,created_at FROM notifications WHERE user_id=$1 AND ($4='all' OR type=$4) ORDER BY created_at DESC LIMIT $2 OFFSET $3`, getPrincipal(r).User.ID, limit, offset, storedType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "알림을 불러올 수 없습니다")
		return
	}
	defer rows.Close()
	items := make([]model.Notification, 0)
	for rows.Next() {
		var item model.Notification
		if err := rows.Scan(&item.ID, &item.UserID, &item.ActorID, &item.Type, &item.TargetID, &item.Payload, &item.ReadAt, &item.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "알림을 불러올 수 없습니다")
			return
		}
		s.decorateNotification(r.Context(), &item)
		items = append(items, item)
	}
	var unread int64
	_ = s.repo.Pool().QueryRow(r.Context(), `SELECT count(*) FROM notifications WHERE user_id=$1 AND read_at IS NULL`, getPrincipal(r).User.ID).Scan(&unread)
	writeData(w, http.StatusOK, map[string]any{"items": items, "unreadCount": unread, "limit": limit, "offset": offset})
}

func (s *Server) decorateNotification(ctx context.Context, item *model.Notification) {
	storedType := item.Type
	labels := map[string]string{
		"follow": "새로운 Link", "reaction": "새로운 Signal", "reply": "새로운 Echo",
		"quote": "새로운 Quote Moin", "remoin": "새로운 Remoin",
		"approval_requested": "검토 요청", "approval_approved": "게시 승인", "approval_rejected": "게시 반려",
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
	case "reaction", "reply", "quote", "remoin", "approval_approved", "approval_rejected":
		postID := item.TargetID
		if value, ok := payload["postId"].(string); ok {
			postID = value
		}
		item.TargetPath = "/moin/" + postID
	case "approval_requested":
		item.TargetPath = "/admin/approvals"
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
		_, err = s.repo.Pool().Exec(r.Context(), `UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE user_id=$1`, getPrincipal(r).User.ID)
	} else {
		_, err = s.repo.Pool().Exec(r.Context(), `UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE user_id=$1 AND id=ANY($2)`, getPrincipal(r).User.ID, input.IDs)
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
	r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(cfg.MaxUploadBytes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_media", "업로드 파일을 읽을 수 없습니다")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file_required", "file 필드가 필요합니다")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, cfg.MaxUploadBytes+1))
	if err != nil || int64(len(data)) > cfg.MaxUploadBytes || len(data) == 0 {
		writeError(w, http.StatusRequestEntityTooLarge, "media_too_large", "파일이 비어 있거나 업로드 한도를 넘었습니다")
		return
	}
	mimeType := detectMediaType(data)
	if !slicesContains([]string{"image/jpeg", "image/png", "image/gif", "image/webp", "video/mp4", "video/webm"}, mimeType) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media", "JPEG, PNG, GIF, WebP 이미지 또는 MP4, WebM 동영상만 업로드할 수 있습니다")
		return
	}
	width, height := imageDimensions(data)
	sum := sha256.Sum256(data)
	mediaType := "image"
	if strings.HasPrefix(mimeType, "video/") {
		mediaType = "video"
	}
	media := model.Media{ID: secure.NewID("media"), OwnerID: getPrincipal(r).User.ID, Filename: safeFilename(header), MIMEType: mimeType, Type: mediaType, Size: int64(len(data)), Width: width, Height: height, CreatedAt: time.Now().UTC()}
	media.URL = "/api/v1/media/" + media.ID
	_, err = s.repo.Pool().Exec(r.Context(), `INSERT INTO media_assets(id,owner_id,filename,mime_type,size_bytes,sha256,width,height,data,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, media.ID, media.OwnerID, media.Filename, media.MIMEType, media.Size, hex.EncodeToString(sum[:]), media.Width, media.Height, data, media.CreatedAt)
	if err != nil {
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

func imageDimensions(data []byte) (int, int) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return config.Width, config.Height
}

func (s *Server) getMedia(w http.ResponseWriter, r *http.Request) {
	viewer := getPrincipal(r).User.ID
	id := chi.URLParam(r, "mediaID")
	var filename, mimeType string
	var data []byte
	var size int64
	err := s.repo.Pool().QueryRow(r.Context(), `SELECT m.filename,m.mime_type,m.size_bytes,m.data FROM media_assets m WHERE m.id=$1 AND (m.owner_id=$2 OR EXISTS(SELECT 1 FROM users au WHERE au.avatar_id=m.id AND au.active AND NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$2 AND b.blocked_id=au.id) OR (b.blocker_id=au.id AND b.blocked_id=$2))) OR EXISTS(SELECT 1 FROM post_media pm JOIN posts p ON p.id=pm.post_id WHERE pm.media_id=m.id AND p.status='published' AND (p.visibility='public' OR p.author_id=$2 OR p.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$2 AND f.followee_id=p.author_id) OR p.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$2))))`, id, viewer).Scan(&filename, &mimeType, &size, &data)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "미디어를 찾을 수 없습니다")
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", strings.ReplaceAll(filename, `"`, "")))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
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
