package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
	"github.com/jackc/pgx/v5"
)

const postAndAuthorColumns = `p.id,p.author_id,p.content,p.kind,p.visibility,p.status,COALESCE(p.reply_to_id,''),COALESCE(p.quote_post_id,''),COALESCE(p.remoin_post_id,''),COALESCE(p.moim_id,''),p.approval_required,p.created_at,p.updated_at,u.id,u.username,u.display_name,u.email,u.bio,u.avatar_id,u.account_type,u.provider,u.roles,u.active,u.created_at,u.updated_at`

type postInput struct {
	Content    string   `json:"content"`
	Visibility string   `json:"visibility"`
	MediaIDs   []string `json:"mediaIds"`
	ReplyToID  string   `json:"replyToId"`
	QuoteID    string   `json:"quoteMoinId"`
	MoimID     string   `json:"moimId"`
}

func scanMoin(row rowScanner) (model.Moin, error) {
	var post model.Moin
	err := row.Scan(
		&post.ID, &post.AuthorID, &post.Content, &post.Kind, &post.Visibility, &post.Status,
		&post.ReplyToID, &post.QuoteMoinID, &post.RemoinMoinID, &post.MoimID, &post.ApprovalRequired, &post.CreatedAt, &post.UpdatedAt,
		&post.Author.ID, &post.Author.Username, &post.Author.DisplayName, &post.Author.Email, &post.Author.Bio, &post.Author.AvatarID,
		&post.Author.AccountType, &post.Author.Provider, &post.Author.Roles, &post.Author.Active, &post.Author.CreatedAt, &post.Author.UpdatedAt,
	)
	return post, err
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
	if len(input.MediaIDs) > 0 {
		var count int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM media_assets WHERE id=ANY($1) AND owner_id=$2`, input.MediaIDs, p.User.ID).Scan(&count); err != nil || count != len(input.MediaIDs) {
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
		if _, err := tx.Exec(r.Context(), `INSERT INTO post_media(post_id,media_id,position) VALUES($1,$2,$3)`, postID, mediaID, index); err != nil {
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
	}
	if err := tx.Commit(r.Context()); err != nil {
		return model.Moin{}, err
	}
	if requiresApproval {
		s.notifyApprovers(r, approvalID, postID, workflow.ApproverRoles)
	}
	if related != nil && related.AuthorID != p.User.ID && status == "published" {
		notificationType := "reply"
		if kind == "quote" {
			notificationType = "quote"
		} else if kind == "remoin" {
			notificationType = "remoin"
		}
		s.notify(r.Context(), related.AuthorID, p.User.ID, notificationType, postID, map[string]string{"postId": postID})
	}
	if status == "published" {
		s.notifyMentions(r.Context(), p.User.ID, postID, input.Content)
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

func (s *Server) notifyMentions(ctx context.Context, actorID, postID, content string) {
	usernames := extractMentions(content)
	if len(usernames) == 0 {
		return
	}
	rows, err := s.repo.Pool().Query(ctx, `SELECT id FROM users WHERE active AND lower(username)=ANY($1)`, usernames)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var userID string
		if rows.Scan(&userID) == nil {
			s.notify(ctx, userID, actorID, "mention", postID, map[string]string{"postId": postID})
		}
	}
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
	if err := s.enrichMoin(ctx, &post, viewerID); err != nil {
		return model.Moin{}, err
	}
	return post, nil
}

func (s *Server) enrichMoin(ctx context.Context, post *model.Moin, viewerID string) error {
	return s.enrichMoinDepth(ctx, post, viewerID, 0)
}

func (s *Server) enrichMoinDepth(ctx context.Context, post *model.Moin, viewerID string, depth int) error {
	var mediaRaw, topicsRaw, signalsRaw, viewerSignalsRaw []byte
	query := `SELECT
		COALESCE((SELECT jsonb_agg(jsonb_build_object('id',m.id,'filename',m.filename,'mimeType',m.mime_type,'size',m.size_bytes,'width',m.width,'height',m.height,'createdAt',m.created_at) ORDER BY pm.position) FROM post_media pm JOIN media_assets m ON m.id=pm.media_id WHERE pm.post_id=$1),'[]'::jsonb),
		COALESCE((SELECT jsonb_agg(jsonb_build_object('id',t.id,'slug',t.slug,'name',t.name,'description',t.description,'createdAt',t.created_at,'following',EXISTS(SELECT 1 FROM user_topic_follows utf WHERE utf.topic_id=t.id AND utf.user_id=$2)) ORDER BY t.name) FROM post_topics pt JOIN topics t ON t.id=pt.topic_id WHERE pt.post_id=$1),'[]'::jsonb),
		COALESCE((SELECT jsonb_object_agg(kind,count) FROM (SELECT kind,count(*) AS count FROM reactions WHERE post_id=$1 GROUP BY kind) counts),'{}'::jsonb),
		COALESCE((SELECT jsonb_agg(kind ORDER BY kind) FROM reactions WHERE post_id=$1 AND user_id=$2),'[]'::jsonb),
		(SELECT count(*) FROM posts WHERE reply_to_id=$1 AND status='published'),
		(SELECT count(*) FROM posts WHERE remoin_post_id=$1 AND status='published'),
		EXISTS(SELECT 1 FROM bookmarks WHERE post_id=$1 AND user_id=$2),
		EXISTS(SELECT 1 FROM follows WHERE follower_id=$2 AND followee_id=$3),
		EXISTS(SELECT 1 FROM posts rp WHERE rp.author_id=$2 AND rp.remoin_post_id=$1 AND rp.status<>'deleted')`
	if err := s.repo.Pool().QueryRow(ctx, query, post.ID, viewerID, post.AuthorID).Scan(&mediaRaw, &topicsRaw, &signalsRaw, &viewerSignalsRaw, &post.ReplyCount, &post.RemoinCount, &post.Bookmarked, &post.Author.Following, &post.ViewerRemoined); err != nil {
		return err
	}
	_ = json.Unmarshal(mediaRaw, &post.Media)
	_ = json.Unmarshal(topicsRaw, &post.Topics)
	_ = json.Unmarshal(signalsRaw, &post.Signals)
	_ = json.Unmarshal(viewerSignalsRaw, &post.ViewerSignals)
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
	post.Why, _ = recommendationComponents(*post, defaultFeedPreferences())
	post.Recommendation = post.Why
	if depth == 0 {
		relatedID := post.QuoteMoinID
		if post.RemoinMoinID != "" {
			relatedID = post.RemoinMoinID
		}
		if relatedID != "" {
			related, err := scanMoin(s.repo.Pool().QueryRow(ctx, `SELECT `+postAndAuthorColumns+` FROM posts p JOIN users u ON u.id=p.author_id WHERE p.id=$1 AND p.status='published' AND NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$2 AND b.blocked_id=p.author_id) OR (b.blocker_id=p.author_id AND b.blocked_id=$2)) AND (p.visibility='public' OR p.author_id=$2 OR p.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$2 AND f.followee_id=p.author_id) OR p.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$2))`, relatedID, viewerID))
			if err == nil && s.enrichMoinDepth(ctx, &related, viewerID, depth+1) == nil {
				if post.RemoinMoinID != "" {
					post.RemoinMoin = &related
				} else {
					post.QuoteMoin = &related
				}
			}
		}
	}
	return nil
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
	viewer := getPrincipal(r).User.ID
	limit, offset := pagination(r)
	where := []string{"p.status='published'", `NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$1 AND b.blocked_id=p.author_id) OR (b.blocker_id=p.author_id AND b.blocked_id=$1))`, `NOT EXISTS(SELECT 1 FROM mutes m WHERE m.muter_id=$1 AND m.muted_id=p.author_id)`, `(p.visibility='public' OR p.author_id=$1 OR p.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1 AND f.followee_id=p.author_id) OR p.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$1))`}
	if mode == "following" {
		where = append(where, `(p.author_id=$1 OR EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1 AND f.followee_id=p.author_id))`)
	}
	fetchLimit := limit
	fetchOffset := offset
	prefs := defaultFeedPreferences()
	if loaded, prefErr := s.loadFeedPreferences(r.Context(), viewer); prefErr == nil {
		prefs = loaded
	}
	if mode == "for_me" && len(prefs.ExcludedTopics) > 0 {
		where = append(where, `NOT EXISTS(SELECT 1 FROM post_topics xpt JOIN topics xt ON xt.id=xpt.topic_id WHERE xpt.post_id=p.id AND xt.slug=ANY($2))`)
	}
	args := []any{viewer}
	if mode == "for_me" && len(prefs.ExcludedTopics) > 0 {
		args = append(args, prefs.ExcludedTopics)
	}
	if mode == "for_me" {
		fetchLimit = 200
		fetchOffset = 0
	}
	items, err := s.queryPosts(r.Context(), where, args, "COALESCE(p.published_at,p.created_at) DESC,p.id DESC", fetchLimit, fetchOffset, viewer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Flow를 불러올 수 없습니다")
		return
	}
	if mode == "for_me" {
		rankingNow := time.Now()
		scores := make(map[string]float64, len(items))
		for index := range items {
			items[index].Why, scores[items[index].ID] = recommendationComponentsAt(items[index], prefs, rankingNow)
			items[index].Recommendation = items[index].Why
		}
		sort.SliceStable(items, func(i, j int) bool {
			iScore, jScore := scores[items[i].ID], scores[items[j].ID]
			if iScore == jScore {
				return items[i].CreatedAt.After(items[j].CreatedAt)
			}
			return iScore > jScore
		})
		if !prefs.ShowReasons {
			for index := range items {
				items[index].Why, items[index].Recommendation = nil, nil
			}
		}
		start := min(offset, len(items))
		end := min(start+limit, len(items))
		items = items[start:end]
	}
	response := listEnvelope(items, limit, offset)
	response["mode"] = mode
	writeData(w, http.StatusOK, response)
}

func (s *Server) queryPosts(ctx context.Context, where []string, args []any, order string, limit, offset int, viewer string) ([]model.Moin, error) {
	args = append(args, limit, offset)
	query := `SELECT ` + postAndAuthorColumns + ` FROM posts p JOIN users u ON u.id=p.author_id WHERE ` + strings.Join(where, " AND ") + fmt.Sprintf(` ORDER BY %s LIMIT $%d OFFSET $%d`, order, len(args)-1, len(args))
	rows, err := s.repo.Pool().Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Moin, 0)
	for rows.Next() {
		post, err := scanMoin(rows)
		if err != nil {
			return nil, err
		}
		if err := s.enrichMoin(ctx, &post, viewer); err != nil {
			return nil, err
		}
		items = append(items, post)
	}
	return items, rows.Err()
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
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" || utf8.RuneCountInString(input.Content) > 5000 || strings.ContainsRune(input.Content, '\x00') {
		writeError(w, http.StatusBadRequest, "invalid_content", "내용은 1~5,000자로 입력해 주세요")
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
	tag, err := s.repo.Pool().Exec(r.Context(), `INSERT INTO reactions(user_id,post_id,kind) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, p.User.ID, postID, input.Type)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "Signal을 저장할 수 없습니다")
		return
	}
	if tag.RowsAffected() > 0 && post.AuthorID != p.User.ID {
		s.notify(r.Context(), post.AuthorID, p.User.ID, "reaction", postID, map[string]string{"postId": postID, "signal": input.Type})
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
