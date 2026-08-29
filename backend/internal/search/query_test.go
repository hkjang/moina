package search

import "testing"

func TestParseNormalizesAndEscapesLike(t *testing.T) {
	query, err := Parse("  Go_100%  ", false)
	if err != nil {
		t.Fatal(err)
	}
	if query.Raw != "Go_100%" || query.Folded != "go_100%" || query.Pattern != `%go\_100\%%` {
		t.Fatalf("query=%+v", query)
	}
}

func TestParseRejectsEmptyUnlessRecommended(t *testing.T) {
	if _, err := Parse(" ", false); err == nil {
		t.Fatal("empty interactive search was accepted")
	}
	if _, err := Parse(" ", true); err != nil {
		t.Fatal(err)
	}
}

func TestParseTreatsHashtagAsExactTopicTerm(t *testing.T) {
	query, err := Parse("#PostgreSQL", false)
	if err != nil {
		t.Fatal(err)
	}
	if query.Raw != "#PostgreSQL" || query.Folded != "postgresql" || query.Pattern != "%postgresql%" {
		t.Fatalf("query=%+v", query)
	}
}

func BenchmarkParse(b *testing.B) {
	for b.Loop() {
		if _, err := Parse("PostgreSQL 성능 튜닝", false); err != nil {
			b.Fatal(err)
		}
	}
}
