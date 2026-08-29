// Package search contains input normalization shared by PostgreSQL search
// handlers. Ranking itself remains in SQL so indexes can be used efficiently.
package search

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrInvalidQuery = errors.New("search query must contain 1 to 100 valid Unicode characters")

type Query struct {
	Raw     string
	Folded  string
	Pattern string
}

func Parse(raw string, allowEmpty bool) (Query, error) {
	raw = strings.TrimSpace(raw)
	length := utf8.RuneCountInString(raw)
	if !utf8.ValidString(raw) || length > 100 || length == 0 && !allowEmpty || strings.ContainsRune(raw, '\x00') {
		return Query{}, ErrInvalidQuery
	}
	folded := strings.ToLower(raw)
	if hashtag := strings.TrimPrefix(folded, "#"); hashtag != folded && hashtag != "" {
		folded = hashtag
	}
	return Query{Raw: raw, Folded: folded, Pattern: "%" + escapeLike(folded) + "%"}, nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
