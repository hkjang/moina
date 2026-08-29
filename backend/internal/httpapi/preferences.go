package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/hkjang/moina/backend/internal/store"
)

const maxPreferencesBytes = 64 * 1024

var preferenceTopicPattern = regexp.MustCompile(`^[\pL\pN_]{1,50}$`)

type appearancePreferences struct {
	Theme        string `json:"theme"`
	FontScale    int    `json:"fontScale"`
	ReduceMotion bool   `json:"reduceMotion"`
	Density      string `json:"density"`
}

type notificationPreferences struct {
	Desktop  bool `json:"desktop"`
	Mentions bool `json:"mentions"`
	Signals  bool `json:"signals"`
	Follows  bool `json:"follows"`
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

type notificationPreferencesPatch struct {
	Desktop  *bool `json:"desktop"`
	Mentions *bool `json:"mentions"`
	Signals  *bool `json:"signals"`
	Follows  *bool `json:"follows"`
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
			Desktop: true, Mentions: true, Signals: true, Follows: true,
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
	current, err := s.loadPreferencesDocument(r.Context(), getPrincipal(r).User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid_preferences", "저장된 개인화 설정이 올바르지 않습니다")
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
	if err := s.repo.PutPreference(r.Context(), getPrincipal(r).User.ID, payload); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error", "개인화 설정을 저장할 수 없습니다")
		return
	}
	s.audit(r, "profile.preferences.update", "user", getPrincipal(r).User.ID, true, nil)
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
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var patch preferencesPatch
	if err := decoder.Decode(&patch); err != nil {
		return preferencesPatch{}, err
	}
	return patch, nil
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
		if patch.Notifications.Desktop != nil {
			current.Notifications.Desktop = *patch.Notifications.Desktop
		}
		if patch.Notifications.Mentions != nil {
			current.Notifications.Mentions = *patch.Notifications.Mentions
		}
		if patch.Notifications.Signals != nil {
			current.Notifications.Signals = *patch.Notifications.Signals
		}
		if patch.Notifications.Follows != nil {
			current.Notifications.Follows = *patch.Notifications.Follows
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
	return nil
}

func slicesClone[T any](source []T) []T {
	if source == nil {
		return []T{}
	}
	return append([]T(nil), source...)
}
