// Package pagination implements opaque, versioned keyset cursors used by Flow.
package pagination

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

const (
	cursorVersion  = 1
	maxCursorBytes = 1024
)

var (
	ErrInvalidCursor          = errors.New("invalid cursor")
	ErrRankingVersionMismatch = errors.New("ranking version mismatch")
)

type Following struct {
	PublishedAt time.Time
	ID          string
}

type ForMe struct {
	AsOf           time.Time
	Score          float64
	ID             string
	RankingVersion string
	SnapshotID     string
}

type cursorPayload struct {
	Version        int     `json:"v"`
	Mode           string  `json:"m"`
	PublishedAt    string  `json:"p,omitempty"`
	AsOf           string  `json:"a,omitempty"`
	Score          float64 `json:"s,omitempty"`
	ID             string  `json:"i"`
	RankingVersion string  `json:"r,omitempty"`
	SnapshotID     string  `json:"f,omitempty"`
}

func EncodeFollowing(value Following) (string, error) {
	if value.PublishedAt.IsZero() || strings.TrimSpace(value.ID) == "" {
		return "", ErrInvalidCursor
	}
	return encode(cursorPayload{Version: cursorVersion, Mode: "following", PublishedAt: value.PublishedAt.UTC().Format(time.RFC3339Nano), ID: value.ID})
}

func DecodeFollowing(encoded string) (Following, error) {
	payload, err := decode(encoded)
	if err != nil || payload.Mode != "following" || payload.PublishedAt == "" || strings.TrimSpace(payload.ID) == "" {
		return Following{}, ErrInvalidCursor
	}
	publishedAt, err := time.Parse(time.RFC3339Nano, payload.PublishedAt)
	if err != nil {
		return Following{}, ErrInvalidCursor
	}
	return Following{PublishedAt: publishedAt.UTC(), ID: payload.ID}, nil
}

func EncodeForMe(value ForMe) (string, error) {
	if value.AsOf.IsZero() || strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.RankingVersion) == "" || strings.TrimSpace(value.SnapshotID) == "" || math.IsNaN(value.Score) || math.IsInf(value.Score, 0) {
		return "", ErrInvalidCursor
	}
	return encode(cursorPayload{Version: cursorVersion, Mode: "for_me", AsOf: value.AsOf.UTC().Format(time.RFC3339Nano), Score: value.Score, ID: value.ID, RankingVersion: value.RankingVersion, SnapshotID: value.SnapshotID})
}

func DecodeForMe(encoded, rankingVersion string) (ForMe, error) {
	payload, err := decode(encoded)
	if err != nil || payload.Mode != "for_me" || payload.AsOf == "" || strings.TrimSpace(payload.ID) == "" || strings.TrimSpace(payload.RankingVersion) == "" || strings.TrimSpace(payload.SnapshotID) == "" || math.IsNaN(payload.Score) || math.IsInf(payload.Score, 0) {
		return ForMe{}, ErrInvalidCursor
	}
	if payload.RankingVersion != rankingVersion {
		return ForMe{}, fmt.Errorf("%w: cursor=%q server=%q", ErrRankingVersionMismatch, payload.RankingVersion, rankingVersion)
	}
	asOf, err := time.Parse(time.RFC3339Nano, payload.AsOf)
	if err != nil {
		return ForMe{}, ErrInvalidCursor
	}
	return ForMe{AsOf: asOf.UTC(), Score: payload.Score, ID: payload.ID, RankingVersion: payload.RankingVersion, SnapshotID: payload.SnapshotID}, nil
}

func encode(payload cursorPayload) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func decode(encoded string) (cursorPayload, error) {
	if encoded == "" || len(encoded) > maxCursorBytes {
		return cursorPayload{}, ErrInvalidCursor
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(body) == 0 || len(body) > maxCursorBytes {
		return cursorPayload{}, ErrInvalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var payload cursorPayload
	if err := decoder.Decode(&payload); err != nil || decoder.More() || payload.Version != cursorVersion {
		return cursorPayload{}, ErrInvalidCursor
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return cursorPayload{}, ErrInvalidCursor
	}
	return payload, nil
}
