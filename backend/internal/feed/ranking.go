// Package feed contains deterministic Flow ranking and paging primitives.
package feed

import "sort"

const (
	// RankingVersion is embedded in every For Me cursor. Change it whenever the
	// score formula or tie-breaking order changes.
	RankingVersion = "for-me-v1"

	// PostListSQLCount documents the fixed round-trip plan for a hydrated list:
	// root posts, visible quote/remoin posts, and all aggregate details.
	PostListSQLCount = 3
)

type Ranked[T any] struct {
	Value T
	ID    string
	Score float64
}

type After struct {
	ID    string
	Score float64
}

// Page sorts by score then stable post ID, both descending. The ID tie-breaker
// makes keyset pagination deterministic even when many posts share a score.
func Page[T any](items []Ranked[T], after *After, limit, legacyOffset int) ([]Ranked[T], bool) {
	ordered := append([]Ranked[T](nil), items...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Score == ordered[j].Score {
			return ordered[i].ID > ordered[j].ID
		}
		return ordered[i].Score > ordered[j].Score
	})
	if after != nil {
		start := 0
		for start < len(ordered) && !comesAfter(ordered[start], *after) {
			start++
		}
		ordered = ordered[start:]
	} else if legacyOffset > 0 {
		if legacyOffset >= len(ordered) {
			return []Ranked[T]{}, false
		}
		ordered = ordered[legacyOffset:]
	}
	if limit < 1 || len(ordered) <= limit {
		return ordered, false
	}
	return ordered[:limit], true
}

func comesAfter[T any](item Ranked[T], cursor After) bool {
	return item.Score < cursor.Score || item.Score == cursor.Score && item.ID < cursor.ID
}
