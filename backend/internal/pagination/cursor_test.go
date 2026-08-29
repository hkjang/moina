package pagination

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFollowingRoundTrip(t *testing.T) {
	want := Following{PublishedAt: time.Date(2026, 8, 29, 9, 12, 13, 456000000, time.FixedZone("KST", 9*60*60)), ID: "moin_02"}
	encoded, err := EncodeFollowing(want)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(encoded, "+/=") {
		t.Fatalf("cursor is not raw Base64URL: %q", encoded)
	}
	got, err := DecodeFollowing(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || !got.PublishedAt.Equal(want.PublishedAt) || got.PublishedAt.Location() != time.UTC {
		t.Fatalf("round trip mismatch: got=%+v want=%+v", got, want)
	}
}

func TestForMeRejectsRankingVersionMismatch(t *testing.T) {
	encoded, err := EncodeForMe(ForMe{AsOf: time.Now(), Score: 42.5, ID: "moin_02", RankingVersion: "for-me-v1", SnapshotID: "feed_01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeForMe(encoded, "for-me-v2"); !errors.Is(err, ErrRankingVersionMismatch) {
		t.Fatalf("expected ranking mismatch, got %v", err)
	}
}

func TestCursorRejectsMalformedAndWrongMode(t *testing.T) {
	for _, encoded := range []string{"", "20", "not-base64", strings.Repeat("a", maxCursorBytes+1), "e30"} {
		if _, err := DecodeFollowing(encoded); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("DecodeFollowing(%q) error=%v", encoded, err)
		}
	}
	encoded, err := EncodeFollowing(Following{PublishedAt: time.Now(), ID: "moin_01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeForMe(encoded, "for-me-v1"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("wrong cursor mode error=%v", err)
	}
}

func BenchmarkForMeCursorRoundTrip(b *testing.B) {
	value := ForMe{AsOf: time.Now(), Score: 123.456, ID: "moin_01J8Q3KQ7CZ", RankingVersion: "for-me-v1", SnapshotID: "feed_01J8Q3KQ7CZ"}
	for b.Loop() {
		encoded, err := EncodeForMe(value)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := DecodeForMe(encoded, value.RankingVersion); err != nil {
			b.Fatal(err)
		}
	}
}
