package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	feedservice "github.com/hkjang/moina/backend/internal/feed"
	"github.com/hkjang/moina/backend/internal/model"
	cursorpage "github.com/hkjang/moina/backend/internal/pagination"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
	"github.com/jackc/pgx/v5"
)

const postAndAuthorColumns = feedservice.PostAndAuthorColumns

type postInput struct {
	Content       string            `json:"content"`
	Visibility    string            `json:"visibility"`
	MediaIDs      []string          `json:"mediaIds"`
	MediaAltTexts map[string]string `json:"mediaAltTexts"`
	ReplyToID     string            `json:"replyToId"`
	QuoteID       string            `json:"quoteMoinId"`
	MoimID        string            `json:"moimId"`
}

func normalizeMediaAltTexts(values map[string]string, attached map[string]bool) *publicError {
	for mediaID, altText := range values {
		trimmed := strings.TrimSpace(altText)
		if strings.TrimSpace(mediaID) == "" || attached != nil && !attached[mediaID] || !utf8.ValidString(altText) || utf8.RuneCountInString(trimmed) > 500 || strings.ContainsRune(altText, '\x00') {
			return &publicError{400, "invalid_media_alt_text", "첨부 미디어의 대체 텍스트는 500자 이하여야 합니다"}
		}
		values[mediaID] = trimmed
	}
	return nil
}

func scanMoin(row rowScanner) (model.Moin, error) {
	return feedservice.ScanMoin(row)
}

func (s *Server) createPost(w http.ResponseWriter, r *http.Request) {
	var input postInput
	if !decodeJSON(w, r, &input) {
		return
	}
	post, err := s.createPostRecord(r, input, "")
	if err != nil {
		writePostError(w, err)
		return
	}
	s.audit(r, "post.create", "post", post.ID, true, map[string]any{"kind": post.Kind, "status": post.Status})
	writeData(w, http.StatusCreated, post)
}

type publicError struct {
	Status  int
	Code    string
	Message string
}

func (e *publicError) Error() string { return e.Message }

func writePostError(w http.ResponseWriter, err error) {
	var public *publicError
	if errors.As(err, &public) {
		writeError(w, public.Status, public.Code, public.Message)
		return
	}
	writeError(w, http.StatusInternalServerError, "storage_error", "Moin을 저장할 수 없습니다")
}

func (s *Server) createPostRecord(r *http.Request, input postInput, forcedKind string) (model.Moin, error) {
	p := getPrincipal(r)
	input.Content = strings.TrimSpace(input.Content)
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	input.ReplyToID = strings.TrimSpace(input.ReplyToID)
	input.QuoteID = strings.TrimSpace(input.QuoteID)
	input.MoimID = strings.TrimSpace(input.MoimID)
	if input.Visibility == "" {
		input.Visibility = "public"
	}
	if !slicesContains([]string{"public", "followers", "moim"}, input.Visibility) {
		return model.Moin{}, &publicError{400, "invalid_visibility", "공개 범위가 올바르지 않습니다"}
	}
	kind := forcedKind
	if kind == "" {
		kind = "moin"
		if input.ReplyToID != "" {
			kind = "echo"
		}
		if input.QuoteID != "" {
			if kind != "moin" {
				return model.Moin{}, &publicError{400, "invalid_relation", "답글과 인용을 동시에 지정할 수 없습니다"}
			}
			kind = "quote"
		}
	}
	if kind != "remoin" && (input.Content == "" || !utf8.ValidString(input.Content) || utf8.RuneCountInString(input.Content) > 5000 || strings.ContainsRune(input.Content, '\x00')) {
		return model.Moin{}, &publicError{400, "invalid_content", "내용은 1~5,000자로 입력해 주세요"}
	}
	if kind == "remoin" {
		input.Content = ""
		var exists bool
		if err := s.repo.Pool().QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM posts WHERE author_id=$1 AND remoin_post_id=$2 AND status<>'deleted')`, p.User.ID, input.QuoteID).Scan(&exists); err != nil {
			return model.Moin{}, err
		}
		if exists {
			return model.Moin{}, &publicError{409, "already_remoined", "이미 Remoin한 Moin입니다"}
		}
	}
	mediaCfg, err := s.mediaSettings(r)
	if err != nil {
		return model.Moin{}, err
	}
	if len(input.MediaIDs) > mediaCfg.MaxPerPost || duplicateStrings(input.MediaIDs) {
		return model.Moin{}, &publicError{400, "invalid_media", "첨부 미디어 수가 한도를 넘었거나 중복되었습니다"}
	}
	attachedMedia := make(map[string]bool, len(input.MediaIDs))
	for _, mediaID := range input.MediaIDs {
		attachedMedia[mediaID] = true
	}
	if public := normalizeMediaAltTexts(input.MediaAltTexts, attachedMedia); public != nil {
		return model.Moin{}, public
	}
	var related *model.Moin
	relatedID := input.ReplyToID
	if kind == "quote" || kind == "remoin" {
		relatedID = input.QuoteID
	}
	if relatedID != "" {
		value, loadErr := s.loadMoin(r.Context(), relatedID, p.User.ID)
		if store.IsNotFound(loadErr) {
			return model.Moin{}, &publicError{404, "related_not_found", "연결할 Moin을 찾을 수 없습니다"}
		}
		if loadErr != nil {
			return model.Moin{}, loadErr
		}
		related = &value
	}
	if input.MoimID != "" || input.Visibility == "moim" {
		if input.MoimID == "" {
			return model.Moin{}, &publicError{400, "moim_required", "Moim 공개 Moin에는 moimId가 필요합니다"}
		}
		var member bool
		if err := s.repo.Pool().QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM moim_members WHERE moim_id=$1 AND user_id=$2)`, input.MoimID, p.User.ID).Scan(&member); err != nil || !member {
			return model.Moin{}, &publicError{403, "moim_membership_required", "가입한 Moim에만 Moin을 작성할 수 있습니다"}
		}
		input.Visibility = "moim"
	}
	workflow, err := s.workflowConfig(r)
	if err != nil {
		return model.Moin{}, err
	}
	requiresApproval := workflowMatches(workflow, "post.publish")
	status := "published"
	if requiresApproval {
		status = "pending_approval"
	}
	postID := secure.NewID("moin")
	tx, err := s.repo.Pool().Begin(r.Context())
	if err != nil {
		return model.Moin{}, err
	}
	defer tx.Rollback(r.Context())
	mediaDefaultAltTexts := make(map[string]string, len(input.MediaIDs))
	if len(input.MediaIDs) > 0 {
		rows, queryErr := tx.Query(r.Context(), `SELECT id,alt_text FROM media_assets WHERE id=ANY($1) AND owner_id=$2`, input.MediaIDs, p.User.ID)
		if queryErr != nil {
			return model.Moin{}, queryErr
		}
		for rows.Next() {
			var mediaID, altText string
			if scanErr := rows.Scan(&mediaID, &altText); scanErr != nil {
				rows.Close()
				return model.Moin{}, scanErr
			}
			mediaDefaultAltTexts[mediaID] = altText
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return model.Moin{}, rowsErr
		}
		if len(mediaDefaultAltTexts) != len(input.MediaIDs) {
			return model.Moin{}, &publicError{400, "invalid_media", "본인이 업로드한 미디어만 첨부할 수 있습니다"}
		}
	}
	var replyID, quoteID, remoinID any
	if input.ReplyToID != "" {
		replyID = input.ReplyToID
	}
	if input.QuoteID != "" && kind == "quote" {
		quoteID = input.QuoteID
	}
	if input.QuoteID != "" && kind == "remoin" {
		remoinID = input.QuoteID
	}
	var moimID any
	if input.MoimID != "" {
		moimID = input.MoimID
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO posts(id,author_id,content,kind,visibility,status,reply_to_id,quote_post_id,remoin_post_id,moim_id,approval_required,published_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,CASE WHEN $6='published' THEN now() END)`, postID, p.User.ID, input.Content, kind, input.Visibility, status, replyID, quoteID, remoinID, moimID, requiresApproval)
	if err != nil {
		return model.Moin{}, err
	}
	for index, mediaID := range input.MediaIDs {
		altText, supplied := input.MediaAltTexts[mediaID]
		if !supplied {
			altText = mediaDefaultAltTexts[mediaID]
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO post_media(post_id,media_id,position,alt_text) VALUES($1,$2,$3,$4)`, postID, mediaID, index, altText); err != nil {
			return model.Moin{}, err
		}
	}
	if err := attachHashtags(r.Context(), tx, postID, input.Content); err != nil {
		return model.Moin{}, err
	}
	approvalID := ""
	if requiresApproval {
		approvalID = secure.NewID("apr")
		snapshot, _ := json.Marshal(map[string]any{"postId": postID, "kind": kind, "contentPreview": truncateRunes(input.Content, 240), "visibility": input.Visibility})
		snapshot, err = s.secrets.Encrypt(snapshot, "approval:"+approvalID+":snapshot")
		if err != nil {
			return model.Moin{}, err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO approval_requests(id,action,target_type,target_id,requester_id,status,snapshot) VALUES($1,'post.publish','post',$2,$3,'pending',$4)`, approvalID, postID, p.User.ID, snapshot); err != nil {
			return model.Moin{}, err
		}
		if err := s.enqueueApproverNotifications(r.Context(), tx, p.User.ID, approvalID, postID, workflow.ApproverRoles); err != nil {
			if errors.Is(err, errNoIndependentApprover) {
				return model.Moin{}, &publicError{http.StatusConflict, "no_independent_approver", "요청자 외의 활성 승인자가 없어 Moin을 승인 대기로 제출할 수 없습니다"}
			}
			return model.Moin{}, err
		}
	}
	if related != nil && related.AuthorID != p.User.ID && status == "published" {
		notificationType := "reply"
		if kind == "quote" {
			notificationType = "quote"
		} else if kind == "remoin" {
			notificationType = "remoin"
		}
		if err := s.enqueueNotification(r.Context(), tx, related.AuthorID, p.User.ID, notificationType, postID,
			map[string]string{"postId": postID}, fmt.Sprintf("notification:post:%s:%s:%s", postID, notificationType, related.AuthorID)); err != nil {
			return model.Moin{}, err
		}
	}
	if status == "published" {
		if err := s.enqueueMentionNotifications(r.Context(), tx, p.User.ID, postID, input.Content); err != nil {
			return model.Moin{}, err
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		return model.Moin{}, err
	}
	return s.loadMoin(r.Context(), postID, p.User.ID)
}

func attachHashtags(ctx context.Context, tx pgx.Tx, postID, content string) error {
	for _, tag := range extractHashtags(content) {
		topicID := secure.NewID("topic")
		var actualID string
		if err := tx.QueryRow(ctx, `INSERT INTO topics(id,slug,name) VALUES($1,$2,$3) ON CONFLICT(slug) DO UPDATE SET name=topics.name RETURNING id`, topicID, tag, tag).Scan(&actualID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO post_topics(post_id,topic_id,source,confidence) VALUES($1,$2,'hashtag',1) ON CONFLICT DO NOTHING`, postID, actualID); err != nil {
			return err
		}
	}
	return nil
}

var hashtagPattern = regexp.MustCompile(`(?i)#[\pL\pN_]{1,50}`)
var mentionPattern = regexp.MustCompile(`(?i)@[\pL\pN][\pL\pN._-]{2,39}`)

func extractHashtags(content string) []string {
	set := map[string]bool{}
	result := make([]string, 0)
	for _, match := range hashtagPattern.FindAllString(content, 20) {
		slug := strings.ToLower(strings.TrimPrefix(match, "#"))
		if !set[slug] {
			set[slug] = true
			result = append(result, slug)
		}
	}
	return result
}

func extractMentions(content string) []string {
	set := map[string]bool{}
	result := make([]string, 0)
	for _, match := range mentionPattern.FindAllString(content, 20) {
		username := strings.ToLower(strings.TrimPrefix(match, "@"))
		if !set[username] {
			set[username] = true
			result = append(result, username)
		}
	}
	return result
}

func (s *Server) getPost(w http.ResponseWriter, r *http.Request) {
	post, err := s.loadMoin(r.Context(), chi.URLParam(r, "postID"), getPrincipal(r).User.ID)
	if store.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "not_found", "Moin을 찾을 수 없습니다")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Moin을 불러올 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, post)
}

func (s *Server) loadMoin(ctx context.Context, postID, viewerID string) (model.Moin, error) {
	query := `SELECT ` + postAndAuthorColumns + ` FROM posts p JOIN users u ON u.id=p.author_id WHERE p.id=$1 AND p.status<>'deleted' AND NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$2 AND b.blocked_id=p.author_id) OR (b.blocker_id=p.author_id AND b.blocked_id=$2)) AND (p.author_id=$2 OR (p.status='published' AND (p.visibility='public' OR p.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$2 AND f.followee_id=p.author_id) OR p.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$2))))`
	post, err := scanMoin(s.repo.Pool().QueryRow(ctx, query, postID, viewerID))
	if err != nil {
		return model.Moin{}, err
	}
	items := []model.Moin{post}
	if err := feedservice.Hydrate(ctx, s.repo.Pool(), items, viewerID); err != nil {
		return model.Moin{}, err
	}
	decorateRecommendations(items)
	return items[0], nil
}

func decorateRecommendations(items []model.Moin) {
	for index := range items {
		items[index].Why, _ = recommendationComponents(items[index], defaultFeedPreferences())
		items[index].Recommendation = items[index].Why
		if items[index].QuoteMoin != nil {
			items[index].QuoteMoin.Why, _ = recommendationComponents(*items[index].QuoteMoin, defaultFeedPreferences())
			items[index].QuoteMoin.Recommendation = items[index].QuoteMoin.Why
		}
		if items[index].RemoinMoin != nil {
			items[index].RemoinMoin.Why, _ = recommendationComponents(*items[index].RemoinMoin, defaultFeedPreferences())
			items[index].RemoinMoin.Recommendation = items[index].RemoinMoin.Why
		}
	}
}

func (s *Server) listPosts(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	viewer := getPrincipal(r).User.ID
	where := []string{"p.status='published'", `NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$1 AND b.blocked_id=p.author_id) OR (b.blocker_id=p.author_id AND b.blocked_id=$1))`, `(p.visibility='public' OR p.author_id=$1 OR p.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1 AND f.followee_id=p.author_id) OR p.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$1))`}
	args := []any{viewer}
	if authorID := strings.TrimSpace(r.URL.Query().Get("authorId")); authorID != "" {
		args = append(args, authorID)
		where = append(where, fmt.Sprintf("p.author_id=$%d", len(args)))
	}
	if author := strings.TrimSpace(r.URL.Query().Get("author")); author != "" {
		args = append(args, author)
		where = append(where, fmt.Sprintf("lower(u.username)=lower($%d)", len(args)))
	}
	if topic := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("topic"))); topic != "" {
		args = append(args, topic)
		where = append(where, fmt.Sprintf("EXISTS(SELECT 1 FROM post_topics pt JOIN topics t ON t.id=pt.topic_id WHERE pt.post_id=p.id AND t.slug=$%d)", len(args)))
	}
	if moimID := strings.TrimSpace(r.URL.Query().Get("moimId")); moimID != "" {
		args = append(args, moimID)
		where = append(where, fmt.Sprintf("p.moim_id=$%d", len(args)))
	}
	if moim := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("moim"))); moim != "" {
		args = append(args, moim)
		where = append(where, fmt.Sprintf("EXISTS(SELECT 1 FROM moims m WHERE m.id=p.moim_id AND m.slug=$%d)", len(args)))
	}
	if r.URL.Query().Get("bookmarked") == "true" {
		where = append(where, "EXISTS(SELECT 1 FROM bookmarks bm WHERE bm.user_id=$1 AND bm.post_id=p.id)")
	}
	items, err := s.queryPosts(r.Context(), where, args, "COALESCE(p.published_at,p.created_at) DESC,p.id DESC", limit, offset, viewer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Moin 목록을 불러올 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, listEnvelope(items, limit, offset))
}

func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode == "" {
		mode = "for_me"
	}
	if mode != "for_me" && mode != "following" {
		writeError(w, http.StatusBadRequest, "invalid_mode", "피드 mode는 for_me 또는 following이어야 합니다")
		return
	}
	s.writeFeed(w, r, mode)
}

func (s *Server) feedAlias(w http.ResponseWriter, r *http.Request) {
	mode := strings.ReplaceAll(strings.ToLower(chi.URLParam(r, "mode")), "-", "_")
	s.writeFeed(w, r, mode)
}

func (s *Server) writeFeed(w http.ResponseWriter, r *http.Request, mode string) {
	if mode != "for_me" && mode != "following" {
		writeError(w, http.StatusBadRequest, "invalid_mode", "피드 mode는 for_me 또는 following이어야 합니다")
		return
	}
	viewer := getPrincipal(r).User.ID
	limit, legacyOffset := pagination(r)
	if legacyOffset != 0 {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "Flow는 offset 대신 서버가 발급한 Cursor를 사용해야 합니다")
		return
	}
	where := []string{"p.status='published'", `NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$1 AND b.blocked_id=p.author_id) OR (b.blocker_id=p.author_id AND b.blocked_id=$1))`, `NOT EXISTS(SELECT 1 FROM mutes m WHERE m.muter_id=$1 AND m.muted_id=p.author_id)`, `(p.visibility='public' OR p.author_id=$1 OR p.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1 AND f.followee_id=p.author_id) OR p.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$1))`}
	if mode == "following" {
		where = append(where, `(p.author_id=$1 OR EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1 AND f.followee_id=p.author_id))`)
		s.writeFollowingFeed(w, r, where, limit, legacyOffset, viewer)
		return
	}
	s.writeForMeFeed(w, r, where, limit, legacyOffset, viewer)
}

func (s *Server) writeFollowingFeed(w http.ResponseWriter, r *http.Request, where []string, limit, legacyOffset int, viewer string) {
	args := []any{viewer}
	where = append(where, "p.published_at IS NOT NULL")
	rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if rawCursor != "" {
		cursor, err := cursorpage.DecodeFollowing(rawCursor)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Following Flow 커서가 올바르지 않습니다")
			return
		}
		legacyOffset = 0
		args = append(args, cursor.PublishedAt, cursor.ID)
		where = append(where, fmt.Sprintf("(p.published_at,p.id)<($%d,$%d)", len(args)-1, len(args)))
	}
	page, err := feedservice.QueryPublishedPosts(r.Context(), s.repo.Pool(), where, args, limit+1, legacyOffset, viewer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Flow를 불러올 수 없습니다")
		return
	}
	hasMore := len(page) > limit
	if hasMore {
		page = page[:limit]
	}
	items := make([]model.Moin, len(page))
	for index := range page {
		items[index] = page[index].Moin
	}
	decorateRecommendations(items)
	nextCursor := ""
	if hasMore && len(page) > 0 {
		nextCursor, err = cursorpage.EncodeFollowing(cursorpage.Following{PublishedAt: page[len(page)-1].PublishedAt, ID: page[len(page)-1].Moin.ID})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cursor_error", "Flow 커서를 만들 수 없습니다")
			return
		}
	}
	writeData(w, http.StatusOK, feedEnvelope(items, limit, legacyOffset, "following", nextCursor))
}

func (s *Server) writeForMeFeed(w http.ResponseWriter, r *http.Request, where []string, limit, legacyOffset int, viewer string) {
	rankingAsOf := time.Now().UTC()
	var after *feedservice.After
	snapshotID := ""
	rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if rawCursor != "" {
		cursor, err := cursorpage.DecodeForMe(rawCursor, feedservice.RankingVersion)
		if err != nil {
			if errors.Is(err, cursorpage.ErrRankingVersionMismatch) {
				writeError(w, http.StatusBadRequest, "ranking_version_mismatch", "추천 기준이 변경되어 Flow를 처음부터 다시 불러와야 합니다")
			} else {
				writeError(w, http.StatusBadRequest, "invalid_cursor", "For Me Flow 커서가 올바르지 않습니다")
			}
			return
		}
		rankingAsOf = cursor.AsOf
		after = &feedservice.After{Score: cursor.Score, ID: cursor.ID}
		snapshotID = cursor.SnapshotID
		legacyOffset = 0
	}
	prefs := defaultFeedPreferences()
	baseWhere := append([]string(nil), where...)
	if snapshotID == "" {
		if loaded, prefErr := s.loadFeedPreferences(r.Context(), viewer); prefErr == nil {
			prefs = loaded
		}
		preferenceJSON, marshalErr := json.Marshal(prefs)
		if marshalErr != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Flow 추천 설정을 처리할 수 없습니다")
			return
		}
		preferenceDigest := sha256.Sum256(preferenceJSON)
		preferredSnapshotID := secure.NewID("feed")
		snapshotWhere := append([]string(nil), baseWhere...)
		snapshotArgs := []any{viewer}
		if len(prefs.ExcludedTopics) > 0 {
			snapshotArgs = append(snapshotArgs, prefs.ExcludedTopics)
			snapshotWhere = append(snapshotWhere, fmt.Sprintf(`NOT EXISTS(SELECT 1 FROM post_topics xpt JOIN topics xt ON xt.id=xpt.topic_id WHERE xpt.post_id=p.id AND xt.slug=ANY($%d))`, len(snapshotArgs)))
		}
		snapshotWhere = append(snapshotWhere, "p.published_at IS NOT NULL", fmt.Sprintf("p.published_at<=$%d", len(snapshotArgs)+1))
		metadata, err := feedservice.CreateOrReuseRankedSnapshot(r.Context(), s.repo.Pool(), preferredSnapshotID, viewer, feedservice.RankingVersion, hex.EncodeToString(preferenceDigest[:]), preferenceJSON,
			snapshotWhere, snapshotArgs,
			feedservice.RankingWeights{Topic: prefs.TopicWeight, Link: prefs.LinkWeight, Discovery: prefs.DiscoveryWeight, Recency: prefs.RecencyWeight},
			rankingAsOf, rankingAsOf.Add(time.Hour))
		if err != nil {
			if errors.Is(err, feedservice.ErrSnapshotBusy) {
				w.Header().Set("Retry-After", "1")
				writeError(w, http.StatusTooManyRequests, "feed_snapshot_busy", "다른 Flow 새로고침을 처리 중입니다. 잠시 후 다시 시도해 주세요")
				return
			}
			writeError(w, http.StatusInternalServerError, "storage_error", "Flow 스냅샷을 만들 수 없습니다")
			return
		}
		snapshotID, rankingAsOf = metadata.ID, metadata.AsOf
		if err := json.Unmarshal(metadata.Preferences, &prefs); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Flow 스냅샷 설정을 읽을 수 없습니다")
			return
		}
	} else {
		metadata, available, err := feedservice.Snapshot(r.Context(), s.repo.Pool(), snapshotID, viewer, feedservice.RankingVersion)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Flow 스냅샷을 확인할 수 없습니다")
			return
		}
		if !available {
			writeError(w, http.StatusBadRequest, "feed_snapshot_expired", "Flow Cursor가 만료되어 처음부터 다시 불러와야 합니다")
			return
		}
		if !metadata.AsOf.Equal(rankingAsOf) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "For Me Flow 커서와 스냅샷 기준 시각이 일치하지 않습니다")
			return
		}
		rankingAsOf = metadata.AsOf
		if err := json.Unmarshal(metadata.Preferences, &prefs); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Flow 스냅샷 설정을 읽을 수 없습니다")
			return
		}
	}
	baseWhere = append(baseWhere, "p.published_at IS NOT NULL")
	ranked, hasMore, err := feedservice.QueryRankedSnapshot(r.Context(), s.repo.Pool(), snapshotID, feedservice.RankingVersion, baseWhere, []any{viewer}, after, limit, viewer)
	if err != nil {
		if errors.Is(err, feedservice.ErrSnapshotUnavailable) {
			writeError(w, http.StatusBadRequest, "feed_snapshot_expired", "Flow Cursor가 만료되어 처음부터 다시 불러와야 합니다")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error", "Flow를 불러올 수 없습니다")
		return
	}
	items := make([]model.Moin, len(ranked))
	for index := range ranked {
		items[index] = ranked[index].Moin
		items[index].Why = snapshotRecommendation(ranked[index])
		items[index].Recommendation = items[index].Why
		if !prefs.ShowReasons {
			items[index].Why, items[index].Recommendation = nil, nil
		}
	}
	nextCursor := ""
	if hasMore && len(ranked) > 0 {
		last := ranked[len(ranked)-1]
		nextCursor, err = cursorpage.EncodeForMe(cursorpage.ForMe{AsOf: rankingAsOf, Score: last.Score, ID: last.Moin.ID, RankingVersion: feedservice.RankingVersion, SnapshotID: snapshotID})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cursor_error", "Flow 커서를 만들 수 없습니다")
			return
		}
	}
	writeData(w, http.StatusOK, feedEnvelope(items, limit, legacyOffset, "for_me", nextCursor))
}

func snapshotRecommendation(item feedservice.RankedMoin) []model.RecommendationReason {
	reasons := make([]model.RecommendationReason, 0, 4)
	if item.LinkScore > 0 {
		reasons = append(reasons, model.RecommendationReason{Label: "Link한 사용자의 Moin", Score: item.LinkScore})
	}
	if item.TopicScore > 0 {
		reasons = append(reasons, model.RecommendationReason{Label: fmt.Sprintf("팔로우한 Topic %d개와 관련", item.FollowedTopics), Score: item.TopicScore})
	}
	if item.DiscoveryScore > 0 {
		reasons = append(reasons, model.RecommendationReason{Label: "새로운 관심사·Signal 발견", Score: item.DiscoveryScore})
	}
	if item.RecencyScore > 0 {
		reasons = append(reasons, model.RecommendationReason{Label: "최근 24시간의 새 Moin", Score: item.RecencyScore})
	}
	return reasons
}

func feedEnvelope(items []model.Moin, limit, offset int, mode, nextCursor string) map[string]any {
	response := map[string]any{"items": items, "limit": limit, "offset": offset, "mode": mode, "rankingVersion": feedservice.RankingVersion}
	if nextCursor != "" {
		response["nextCursor"] = nextCursor
	}
	return response
}

func (s *Server) queryPosts(ctx context.Context, where []string, args []any, order string, limit, offset int, viewer string) ([]model.Moin, error) {
	items, err := feedservice.QueryPosts(ctx, s.repo.Pool(), where, args, order, limit, offset, viewer)
	if err != nil {
		return nil, err
	}
	decorateRecommendations(items)
	return items, nil
}

func listEnvelope(items []model.Moin, limit, offset int) map[string]any {
	response := map[string]any{"items": items, "limit": limit, "offset": offset}
	if len(items) == limit {
		response["nextCursor"] = fmt.Sprintf("%d", offset+limit)
	}
	return response
}

func recommendationComponents(post model.Moin, prefs feedPreferences) ([]model.RecommendationReason, float64) {
	return recommendationComponentsAt(post, prefs, time.Now())
}

func recommendationComponentsAt(post model.Moin, prefs feedPreferences, now time.Time) ([]model.RecommendationReason, float64) {
	reasons := make([]model.RecommendationReason, 0, 4)
	total := 0.0
	if post.Author.Following && prefs.LinkWeight > 0 {
		reasons = append(reasons, model.RecommendationReason{Label: "Link한 사용자의 Moin", Score: prefs.LinkWeight})
		total += prefs.LinkWeight
	}
	followedTopics := 0
	for _, topic := range post.Topics {
		if topic.Following {
			followedTopics++
		}
	}
	if followedTopics > 0 && prefs.TopicWeight > 0 {
		score := float64(followedTopics) * prefs.TopicWeight
		reasons = append(reasons, model.RecommendationReason{Label: fmt.Sprintf("팔로우한 Topic %d개와 관련", followedTopics), Score: score})
		total += score
	}
	discoveryBase := 0.0
	for kind, count := range post.Signals {
		weight := 1.0
		if kind == "useful" || kind == "insight" || kind == "verify" {
			weight = 2
		}
		discoveryBase += float64(count) * weight
	}
	if !post.Author.Following && followedTopics == 0 {
		discoveryBase++
	}
	if discoveryBase > 0 && prefs.DiscoveryWeight > 0 {
		score := discoveryBase * (prefs.DiscoveryWeight / 10)
		reasons = append(reasons, model.RecommendationReason{Label: "새로운 관심사·Signal 발견", Score: score})
		total += score
	}
	ageHours := now.Sub(post.CreatedAt).Hours()
	if ageHours < 24 && prefs.RecencyWeight > 0 {
		score := max(0, (24-ageHours)/24) * prefs.RecencyWeight
		reasons = append(reasons, model.RecommendationReason{Label: "최근 24시간의 새 Moin", Score: score})
		total += score
	}
	return reasons, total
}

type feedPreferences struct {
	Mode            string   `json:"mode"`
	TopicWeight     float64  `json:"topicWeight"`
	LinkWeight      float64  `json:"linkWeight"`
	DiscoveryWeight float64  `json:"discoveryWeight"`
	RecencyWeight   float64  `json:"recencyWeight"`
	ExcludedTopics  []string `json:"excludedTopics"`
	ShowReasons     bool     `json:"showReasons"`
}

func defaultFeedPreferences() feedPreferences {
	return feedPreferences{Mode: "for_me", TopicWeight: 30, LinkWeight: 30, DiscoveryWeight: 10, RecencyWeight: 30, ExcludedTopics: []string{}, ShowReasons: true}
}

func (s *Server) loadFeedPreferences(ctx context.Context, userID string) (feedPreferences, error) {
	value, err := s.loadPreferencesDocument(ctx, userID)
	if err != nil {
		return feedPreferences{}, err
	}
	return value.Feed, nil
}

func recommendationScore(post model.Moin, prefs feedPreferences) float64 {
	_, score := recommendationComponents(post, prefs)
	return score
}

func (s *Server) listReplies(w http.ResponseWriter, r *http.Request) {
	parentID := chi.URLParam(r, "postID")
	if _, err := s.loadMoin(r.Context(), parentID, getPrincipal(r).User.ID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Moin을 찾을 수 없습니다")
		return
	}
	limit, offset := pagination(r)
	viewer := getPrincipal(r).User.ID
	items, err := s.queryPosts(r.Context(), []string{"p.status='published'", "p.reply_to_id=$2", `NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$1 AND b.blocked_id=p.author_id) OR (b.blocker_id=p.author_id AND b.blocked_id=$1))`, `(p.visibility='public' OR p.author_id=$1 OR p.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1 AND f.followee_id=p.author_id) OR p.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$1))`}, []any{viewer, parentID}, "p.created_at ASC,p.id ASC", limit, offset, viewer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Echo 목록을 불러올 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, listEnvelope(items, limit, offset))
}

func (s *Server) updatePost(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	id := chi.URLParam(r, "postID")
	var input struct {
		Content       string            `json:"content"`
		MediaAltTexts map[string]string `json:"mediaAltTexts"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" || utf8.RuneCountInString(input.Content) > 5000 || strings.ContainsRune(input.Content, '\x00') {
		writeError(w, http.StatusBadRequest, "invalid_content", "내용은 1~5,000자로 입력해 주세요")
		return
	}
	if public := normalizeMediaAltTexts(input.MediaAltTexts, nil); public != nil {
		writePostError(w, public)
		return
	}
	tx, err := s.repo.Pool().Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Moin을 변경할 수 없습니다")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), `UPDATE posts SET content=$3,updated_at=now() WHERE id=$1 AND author_id=$2 AND status='published' AND kind<>'remoin'`, id, p.User.ID, input.Content)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "not_editable", "본인의 공개 Moin만 수정할 수 있습니다")
		return
	}
	if len(input.MediaAltTexts) > 0 {
		mediaIDs := make([]string, 0, len(input.MediaAltTexts))
		altTexts := make([]string, 0, len(input.MediaAltTexts))
		for mediaID, altText := range input.MediaAltTexts {
			mediaIDs = append(mediaIDs, mediaID)
			altTexts = append(altTexts, altText)
		}
		mediaTag, updateErr := tx.Exec(r.Context(), `UPDATE post_media pm SET alt_text=v.alt_text FROM unnest($1::text[],$2::text[]) AS v(id,alt_text) WHERE pm.post_id=$3 AND pm.media_id=v.id`, mediaIDs, altTexts, id)
		if updateErr != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "미디어 대체 텍스트를 변경할 수 없습니다")
			return
		}
		if mediaTag.RowsAffected() != int64(len(mediaIDs)) {
			writeError(w, http.StatusBadRequest, "invalid_media_alt_text", "첨부된 미디어의 대체 텍스트만 변경할 수 있습니다")
			return
		}
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM post_topics WHERE post_id=$1 AND source='hashtag'`, id); err != nil || attachHashtags(r.Context(), tx, id, input.Content) != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Moin을 변경할 수 없습니다")
		return
	}
	post, err := s.loadMoin(r.Context(), id, p.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "변경된 Moin을 불러올 수 없습니다")
		return
	}
	s.audit(r, "post.update", "post", id, true, nil)
	writeData(w, http.StatusOK, post)
}

func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	id := chi.URLParam(r, "postID")
	tx, err := s.repo.Pool().Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Moin을 삭제할 수 없습니다")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), `UPDATE posts SET status='deleted',content='',deleted_at=now(),updated_at=now() WHERE id=$1 AND (author_id=$2 OR $3) AND status<>'deleted'`, id, p.User.ID, hasPermission(p.Permissions, "posts:manage"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Moin을 삭제할 수 없습니다")
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE approval_requests SET status='cancelled',reviewed_at=now(),comment='Moin 삭제로 자동 취소' WHERE target_type='post' AND target_id=$1 AND status='pending'`, id); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Moin을 삭제할 수 없습니다")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "삭제할 Moin을 찾을 수 없습니다")
		return
	}
	s.audit(r, "post.delete", "post", id, true, nil)
	w.WriteHeader(http.StatusNoContent)
}

var signalKinds = []string{"like", "useful", "insight", "question", "verify"}

func (s *Server) putReaction(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	postID := chi.URLParam(r, "postID")
	var input struct {
		Type string `json:"type"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	if !slicesContains(signalKinds, input.Type) {
		writeError(w, http.StatusBadRequest, "invalid_signal", "지원하지 않는 Signal입니다")
		return
	}
	post, err := s.loadMoin(r.Context(), postID, p.User.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Moin을 찾을 수 없습니다")
		return
	}
	tx, err := s.repo.Pool().Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Signal을 저장할 수 없습니다")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), `INSERT INTO reactions(user_id,post_id,kind) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, p.User.ID, postID, input.Type)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Signal을 저장할 수 없습니다")
		return
	}
	if tag.RowsAffected() > 0 && post.AuthorID != p.User.ID {
		if err := s.enqueueNotification(r.Context(), tx, post.AuthorID, p.User.ID, "reaction", postID,
			map[string]string{"postId": postID, "signal": input.Type},
			"notification:reaction:"+secure.NewID("op")); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "Signal을 저장할 수 없습니다")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Signal을 저장할 수 없습니다")
		return
	}
	post, _ = s.loadMoin(r.Context(), postID, p.User.ID)
	writeData(w, http.StatusOK, map[string]any{"type": input.Type, "signals": post.Signals, "viewerSignals": post.ViewerSignals})
}

func (s *Server) deleteReaction(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	kind := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "reaction")))
	if kind == "" && r.ContentLength != 0 {
		var input struct {
			Type string `json:"type"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		kind = strings.ToLower(strings.TrimSpace(input.Type))
	}
	if !slicesContains(signalKinds, kind) {
		writeError(w, http.StatusBadRequest, "invalid_signal", "지원하지 않는 Signal입니다")
		return
	}
	_, err := s.repo.Pool().Exec(r.Context(), `DELETE FROM reactions WHERE user_id=$1 AND post_id=$2 AND kind=$3`, p.User.ID, chi.URLParam(r, "postID"), kind)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Signal을 삭제할 수 없습니다")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putBookmark(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	postID := chi.URLParam(r, "postID")
	if _, err := s.loadMoin(r.Context(), postID, p.User.ID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Moin을 찾을 수 없습니다")
		return
	}
	if _, err := s.repo.Pool().Exec(r.Context(), `INSERT INTO bookmarks(user_id,post_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, p.User.ID, postID); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Pocket에 저장할 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, map[string]bool{"bookmarked": true})
}

func (s *Server) deleteBookmark(w http.ResponseWriter, r *http.Request) {
	_, err := s.repo.Pool().Exec(r.Context(), `DELETE FROM bookmarks WHERE user_id=$1 AND post_id=$2`, getPrincipal(r).User.ID, chi.URLParam(r, "postID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Pocket에서 삭제할 수 없습니다")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) remoinPost(w http.ResponseWriter, r *http.Request) {
	target := chi.URLParam(r, "postID")
	post, err := s.createPostRecord(r, postInput{QuoteID: target, Visibility: "public"}, "remoin")
	if err != nil {
		writePostError(w, err)
		return
	}
	s.audit(r, "post.remoin", "post", post.ID, true, map[string]string{"sourcePostId": target})
	writeData(w, http.StatusCreated, post)
}

func (s *Server) deleteRemoin(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	target := chi.URLParam(r, "postID")
	tx, err := s.repo.Pool().Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Remoin을 취소할 수 없습니다")
		return
	}
	defer tx.Rollback(r.Context())
	var remoinID string
	err = tx.QueryRow(r.Context(), `UPDATE posts SET status='deleted',deleted_at=now(),updated_at=now() WHERE author_id=$1 AND remoin_post_id=$2 AND status<>'deleted' RETURNING id`, p.User.ID, target).Scan(&remoinID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "취소할 Remoin이 없습니다")
		} else {
			writeError(w, http.StatusInternalServerError, "storage_error", "Remoin을 취소할 수 없습니다")
		}
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE approval_requests SET status='cancelled',reviewed_at=now(),comment='Remoin 취소로 자동 취소' WHERE target_type='post' AND target_id=$1 AND status='pending'`, remoinID); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Remoin을 취소할 수 없습니다")
		return
	}
	s.audit(r, "post.remoin.delete", "post", target, true, nil)
	w.WriteHeader(http.StatusNoContent)
}

func duplicateStrings(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
