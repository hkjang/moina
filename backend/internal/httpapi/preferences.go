package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hkjang/moina/backend/internal/store"
	"github.com/jackc/pgx/v5"
)

const maxPreferencesBytes = 64 * 1024

var preferenceTopicPattern = regexp.MustCompile(`^[\pL\pN_]{1,50}$`)

type appearancePreferences struct {
	Theme        string `json:"theme"`
	FontScale    int    `json:"fontScale"`
	ReduceMotion bool   `json:"reduceMotion"`
	Density      string `json:"density"`
}

type notificationTypePreferences struct {
	Mentions  bool `json:"mentions"`
	Signals   bool `json:"signals"`
	Follows   bool `json:"follows"`
	Echoes    bool `json:"echoes"`
	Approvals bool `json:"approvals"`
}

type notificationChannelPreferences struct {
	Enabled bool `json:"enabled"`
}

type notificationDigestPreferences struct {
	Mode string `json:"mode"`
	Time string `json:"time"`
}

type notificationQuietHoursPreferences struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

type notificationPreferences struct {
	InApp      notificationTypePreferences       `json:"inApp"`
	Toast      notificationChannelPreferences    `json:"toast"`
	Desktop    notificationChannelPreferences    `json:"desktop"`
	Email      notificationChannelPreferences    `json:"email"`
	Digest     notificationDigestPreferences     `json:"digest"`
	QuietHours notificationQuietHoursPreferences `json:"quietHours"`
}

type preferencesDocument struct {
	Appearance    appearancePreferences   `json:"appearance"`
	Feed          feedPreferences         `json:"feed"`
	Notifications notificationPreferences `json:"notifications"`
}

type appearancePreferencesPatch struct {
	Theme        *string `json:"theme"`
	FontScale    *int    `json:"fontScale"`
	ReduceMotion *bool   `json:"reduceMotion"`
	Density      *string `json:"density"`
}

type feedPreferencesPatch struct {
	Mode            *string   `json:"mode"`
	TopicWeight     *float64  `json:"topicWeight"`
	LinkWeight      *float64  `json:"linkWeight"`
	DiscoveryWeight *float64  `json:"discoveryWeight"`
	RecencyWeight   *float64  `json:"recencyWeight"`
	ExcludedTopics  *[]string `json:"excludedTopics"`
	ShowReasons     *bool     `json:"showReasons"`
}

type notificationTypePreferencesPatch struct {
	Mentions  *bool `json:"mentions"`
	Signals   *bool `json:"signals"`
	Follows   *bool `json:"follows"`
	Echoes    *bool `json:"echoes"`
	Approvals *bool `json:"approvals"`
}

type notificationChannelPreferencesPatch struct {
	Enabled *bool `json:"enabled"`
}

type notificationDigestPreferencesPatch struct {
	Mode *string `json:"mode"`
	Time *string `json:"time"`
}

type notificationQuietHoursPreferencesPatch struct {
	Enabled *bool   `json:"enabled"`
	Start   *string `json:"start"`
	End     *string `json:"end"`
}

type notificationPreferencesPatch struct {
	InApp      *notificationTypePreferencesPatch       `json:"inApp"`
	Toast      *notificationChannelPreferencesPatch    `json:"toast"`
	Desktop    *notificationChannelPreferencesPatch    `json:"desktop"`
	Email      *notificationChannelPreferencesPatch    `json:"email"`
	Digest     *notificationDigestPreferencesPatch     `json:"digest"`
	QuietHours *notificationQuietHoursPreferencesPatch `json:"quietHours"`
}

type preferencesPatch struct {
	Appearance    *appearancePreferencesPatch   `json:"appearance"`
	Feed          *feedPreferencesPatch         `json:"feed"`
	Notifications *notificationPreferencesPatch `json:"notifications"`
}

func defaultPreferencesDocument() preferencesDocument {
	return preferencesDocument{
		Appearance: appearancePreferences{Theme: "system", FontScale: 112, Density: "comfortable"},
		Feed:       defaultFeedPreferences(),
		Notifications: notificationPreferences{
			InApp: notificationTypePreferences{Mentions: true, Signals: true, Follows: true, Echoes: true, Approvals: true},
			Toast: notificationChannelPreferences{Enabled: true}, Desktop: notificationChannelPreferences{Enabled: false}, Email: notificationChannelPreferences{Enabled: false},
			Digest:     notificationDigestPreferences{Mode: "off", Time: "08:00"},
			QuietHours: notificationQuietHoursPreferences{Enabled: false, Start: "22:00", End: "07:00"},
		},
	}
}

func (s *Server) getPreferences(w http.ResponseWriter, r *http.Request) {
	value, err := s.loadPreferencesDocument(r.Context(), getPrincipal(r).User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid_preferences", "개인화 설정을 불러올 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, value)
}

// putPreferences accepts a partial envelope and persists a complete, validated
// document. This lets each settings screen update its own section without
// erasing preferences owned by another screen.
func (s *Server) putPreferences(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return
	}
	if len(raw) > maxPreferencesBytes {
		writeError(w, http.StatusBadRequest, "preferences_too_large", "개인화 설정이 너무 큽니다")
		return
	}
	patch, err := decodePreferencesPatch(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_preferences", "개인화 설정 형식이 올바르지 않습니다")
		return
	}
	userID := getPrincipal(r).User.ID
	tx, err := s.repo.Pool().Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "개인화 설정을 저장할 수 없습니다")
		return
	}
	defer tx.Rollback(r.Context())
	current := defaultPreferencesDocument()
	var stored json.RawMessage
	err = tx.QueryRow(r.Context(), `SELECT payload FROM user_preferences WHERE user_id=$1 FOR UPDATE`, userID).Scan(&stored)
	if err == nil {
		storedPatch, decodeErr := decodePreferencesPatch(stored)
		if decodeErr != nil {
			writeError(w, http.StatusInternalServerError, "invalid_preferences", "저장된 개인화 설정이 올바르지 않습니다")
			return
		}
		current, err = applyPreferencesPatch(current, storedPatch)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invalid_preferences", "저장된 개인화 설정이 올바르지 않습니다")
			return
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "storage_error", "개인화 설정을 저장할 수 없습니다")
		return
	}
	updated, err := applyPreferencesPatch(current, patch)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_preferences", "개인화 설정 값의 허용 범위를 확인해 주세요")
		return
	}
	payload, err := json.Marshal(updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding_error", "개인화 설정을 처리할 수 없습니다")
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO user_preferences(user_id,payload) VALUES($1,$2)
		ON CONFLICT(user_id) DO UPDATE SET payload=EXCLUDED.payload,updated_at=now()`, userID, payload); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "개인화 설정을 저장할 수 없습니다")
		return
	}
	oldDigestSignature := digestConfigSignature(current.Notifications.Digest)
	newDigestSignature := digestConfigSignature(updated.Notifications.Digest)
	if oldDigestSignature != newDigestSignature {
		// Digest schedule transitions are subscription boundaries. Mark the
		// existing backlog and state in the same transaction as the preference
		// update so off-period events cannot be replayed after re-enabling it.
		if _, err := tx.Exec(r.Context(), `INSERT INTO notification_digest_state(user_id,last_sent_at,config_signature) VALUES($1,now(),$2)
			ON CONFLICT(user_id) DO UPDATE SET last_sent_at=EXCLUDED.last_sent_at,config_signature=EXCLUDED.config_signature,updated_at=now()`, userID, newDigestSignature); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "개인화 설정을 저장할 수 없습니다")
			return
		}
		if _, err := tx.Exec(r.Context(), `UPDATE notifications SET digested_at=now()
			WHERE user_id=$1 AND type<>'digest' AND digested_at IS NULL`, userID); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error", "개인화 설정을 저장할 수 없습니다")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "개인화 설정을 저장할 수 없습니다")
		return
	}
	s.audit(r, "profile.preferences.update", "user", userID, true, nil)
	writeData(w, http.StatusOK, updated)
}

func (s *Server) loadPreferencesDocument(ctx context.Context, userID string) (preferencesDocument, error) {
	value, err := s.repo.GetPreference(ctx, userID)
	if store.IsNotFound(err) {
		return defaultPreferencesDocument(), nil
	}
	if err != nil {
		return preferencesDocument{}, err
	}
	patch, err := decodePreferencesPatch(value)
	if err != nil {
		return preferencesDocument{}, err
	}
	return applyPreferencesPatch(defaultPreferencesDocument(), patch)
}

func decodePreferencesPatch(raw json.RawMessage) (preferencesPatch, error) {
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil || tree == nil || containsJSONNull(tree) {
		return preferencesPatch{}, errors.New("preferences must be a non-null object")
	}
	// v0.1.2 stored notification switches directly below `notifications` and
	// represented desktop as a boolean. Normalize that persisted shape before
	// strict decoding so upgrades preserve each user's choices.
	normalizeLegacyNotificationPreferences(tree)
	normalized, err := json.Marshal(tree)
	if err != nil {
		return preferencesPatch{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(normalized)))
	decoder.DisallowUnknownFields()
	var patch preferencesPatch
	if err := decoder.Decode(&patch); err != nil {
		return preferencesPatch{}, err
	}
	return patch, nil
}

func normalizeLegacyNotificationPreferences(tree any) {
	root, ok := tree.(map[string]any)
	if !ok {
		return
	}
	notifications, ok := root["notifications"].(map[string]any)
	if !ok {
		return
	}
	inApp, _ := notifications["inApp"].(map[string]any)
	if inApp == nil {
		inApp = map[string]any{}
	}
	for _, key := range []string{"mentions", "signals", "follows", "echoes", "approvals"} {
		if value, exists := notifications[key]; exists {
			if _, alreadySet := inApp[key]; !alreadySet {
				inApp[key] = value
			}
			delete(notifications, key)
		}
	}
	if len(inApp) > 0 {
		notifications["inApp"] = inApp
	}
	if _, legacy := notifications["desktop"].(bool); legacy {
		// v0.1.2 persisted a placeholder default=true but never requested
		// browser permission or delivered desktop notifications. Activating the
		// new channel from that value would bypass explicit user consent.
		notifications["desktop"] = map[string]any{"enabled": false}
	}
}

func containsJSONNull(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case []any:
		for _, item := range typed {
			if containsJSONNull(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if containsJSONNull(item) {
				return true
			}
		}
	}
	return false
}

func applyPreferencesPatch(current preferencesDocument, patch preferencesPatch) (preferencesDocument, error) {
	if patch.Appearance != nil {
		if patch.Appearance.Theme != nil {
			current.Appearance.Theme = *patch.Appearance.Theme
		}
		if patch.Appearance.FontScale != nil {
			current.Appearance.FontScale = *patch.Appearance.FontScale
		}
		if patch.Appearance.ReduceMotion != nil {
			current.Appearance.ReduceMotion = *patch.Appearance.ReduceMotion
		}
		if patch.Appearance.Density != nil {
			current.Appearance.Density = *patch.Appearance.Density
		}
	}
	if patch.Feed != nil {
		if patch.Feed.Mode != nil {
			current.Feed.Mode = *patch.Feed.Mode
		}
		if patch.Feed.TopicWeight != nil {
			current.Feed.TopicWeight = *patch.Feed.TopicWeight
		}
		if patch.Feed.LinkWeight != nil {
			current.Feed.LinkWeight = *patch.Feed.LinkWeight
		}
		if patch.Feed.DiscoveryWeight != nil {
			current.Feed.DiscoveryWeight = *patch.Feed.DiscoveryWeight
		}
		if patch.Feed.RecencyWeight != nil {
			current.Feed.RecencyWeight = *patch.Feed.RecencyWeight
		}
		if patch.Feed.ExcludedTopics != nil {
			current.Feed.ExcludedTopics = slicesClone(*patch.Feed.ExcludedTopics)
		}
		if patch.Feed.ShowReasons != nil {
			current.Feed.ShowReasons = *patch.Feed.ShowReasons
		}
	}
	if patch.Notifications != nil {
		if patch.Notifications.InApp != nil {
			if patch.Notifications.InApp.Mentions != nil {
				current.Notifications.InApp.Mentions = *patch.Notifications.InApp.Mentions
			}
			if patch.Notifications.InApp.Signals != nil {
				current.Notifications.InApp.Signals = *patch.Notifications.InApp.Signals
			}
			if patch.Notifications.InApp.Follows != nil {
				current.Notifications.InApp.Follows = *patch.Notifications.InApp.Follows
			}
			if patch.Notifications.InApp.Echoes != nil {
				current.Notifications.InApp.Echoes = *patch.Notifications.InApp.Echoes
			}
			if patch.Notifications.InApp.Approvals != nil {
				current.Notifications.InApp.Approvals = *patch.Notifications.InApp.Approvals
			}
		}
		if patch.Notifications.Toast != nil && patch.Notifications.Toast.Enabled != nil {
			current.Notifications.Toast.Enabled = *patch.Notifications.Toast.Enabled
		}
		if patch.Notifications.Desktop != nil && patch.Notifications.Desktop.Enabled != nil {
			current.Notifications.Desktop.Enabled = *patch.Notifications.Desktop.Enabled
		}
		if patch.Notifications.Email != nil && patch.Notifications.Email.Enabled != nil {
			current.Notifications.Email.Enabled = *patch.Notifications.Email.Enabled
		}
		if patch.Notifications.Digest != nil {
			if patch.Notifications.Digest.Mode != nil {
				current.Notifications.Digest.Mode = *patch.Notifications.Digest.Mode
			}
			if patch.Notifications.Digest.Time != nil {
				current.Notifications.Digest.Time = *patch.Notifications.Digest.Time
			}
		}
		if patch.Notifications.QuietHours != nil {
			if patch.Notifications.QuietHours.Enabled != nil {
				current.Notifications.QuietHours.Enabled = *patch.Notifications.QuietHours.Enabled
			}
			if patch.Notifications.QuietHours.Start != nil {
				current.Notifications.QuietHours.Start = *patch.Notifications.QuietHours.Start
			}
			if patch.Notifications.QuietHours.End != nil {
				current.Notifications.QuietHours.End = *patch.Notifications.QuietHours.End
			}
		}
	}
	if err := validateAndNormalizePreferences(&current); err != nil {
		return preferencesDocument{}, err
	}
	return current, nil
}

func validateAndNormalizePreferences(value *preferencesDocument) error {
	if value.Appearance.Theme != "light" && value.Appearance.Theme != "dark" && value.Appearance.Theme != "system" {
		return errors.New("invalid theme")
	}
	if value.Appearance.FontScale != 100 && value.Appearance.FontScale != 112 && value.Appearance.FontScale != 125 {
		return errors.New("invalid font scale")
	}
	if value.Appearance.Density != "comfortable" && value.Appearance.Density != "compact" {
		return errors.New("invalid density")
	}
	if value.Feed.Mode != "for_me" && value.Feed.Mode != "following" {
		return errors.New("invalid feed mode")
	}
	for _, weight := range []float64{value.Feed.TopicWeight, value.Feed.LinkWeight, value.Feed.DiscoveryWeight, value.Feed.RecencyWeight} {
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 || weight > 100 {
			return errors.New("invalid feed weight")
		}
	}
	if len(value.Feed.ExcludedTopics) > 100 {
		return errors.New("too many excluded topics")
	}
	seen := make(map[string]bool, len(value.Feed.ExcludedTopics))
	normalized := make([]string, 0, len(value.Feed.ExcludedTopics))
	for _, topic := range value.Feed.ExcludedTopics {
		topic = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(topic), "#"))
		if !preferenceTopicPattern.MatchString(topic) {
			return errors.New("invalid excluded topic")
		}
		if !seen[topic] {
			seen[topic] = true
			normalized = append(normalized, topic)
		}
	}
	value.Feed.ExcludedTopics = normalized
	if value.Notifications.Digest.Mode != "off" && value.Notifications.Digest.Mode != "hourly" && value.Notifications.Digest.Mode != "daily" {
		return errors.New("invalid notification digest mode")
	}
	if !validClockTime(value.Notifications.Digest.Time) || !validClockTime(value.Notifications.QuietHours.Start) || !validClockTime(value.Notifications.QuietHours.End) {
		return errors.New("invalid notification time")
	}
	// Approval and security-related notices are mandatory operational records.
	value.Notifications.InApp.Approvals = true
	return nil
}

func validClockTime(value string) bool {
	if len(value) != 5 || value[2] != ':' {
		return false
	}
	parsed, err := time.Parse("15:04", value)
	return err == nil && parsed.Format("15:04") == value
}

func notificationInAppEnabled(value notificationPreferences, kind string) bool {
	switch strings.TrimSpace(kind) {
	case "mention":
		return value.InApp.Mentions
	case "reaction":
		return value.InApp.Signals
	case "follow":
		return value.InApp.Follows
	case "reply", "quote", "remoin":
		return value.InApp.Echoes
	case "approval_requested", "approval_approved", "approval_rejected", "security":
		return true
	default:
		// New operational notification types are kept until an explicit user
		// category is introduced, preventing a rollout from silently losing data.
		return true
	}
}

func notificationEmailEnabled(value notificationPreferences, kind string) bool {
	if !value.Email.Enabled {
		return false
	}
	if kind == "digest" {
		return value.Digest.Mode != "off"
	}
	return notificationInAppEnabled(value, kind) && !notificationBatched(value, kind)
}

func notificationQuietAt(value notificationQuietHoursPreferences, now time.Time) bool {
	if !value.Enabled || !validClockTime(value.Start) || !validClockTime(value.End) || value.Start == value.End {
		return false
	}
	minutes := now.Hour()*60 + now.Minute()
	start := int(value.Start[0]-'0')*600 + int(value.Start[1]-'0')*60 + int(value.Start[3]-'0')*10 + int(value.Start[4]-'0')
	end := int(value.End[0]-'0')*600 + int(value.End[1]-'0')*60 + int(value.End[3]-'0')*10 + int(value.End[4]-'0')
	if start < end {
		return minutes >= start && minutes < end
	}
	return minutes >= start || minutes < end
}

func slicesClone[T any](source []T) []T {
	if source == nil {
		return []T{}
	}
	return append([]T(nil), source...)
}
