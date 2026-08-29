package feed

import (
	"context"
	"slices"
	"testing"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPageUsesScoreAndIDKeyset(t *testing.T) {
	items := []Ranked[string]{
		{Value: "a", ID: "moin_a", Score: 20},
		{Value: "c", ID: "moin_c", Score: 20},
		{Value: "b", ID: "moin_b", Score: 20},
		{Value: "d", ID: "moin_d", Score: 10},
	}
	first, more := Page(items, nil, 2, 0)
	if !more || !slices.Equal(values(first), []string{"c", "b"}) {
		t.Fatalf("first=%v more=%v", values(first), more)
	}
	second, more := Page(items, &After{Score: first[1].Score, ID: first[1].ID}, 2, 0)
	if more || !slices.Equal(values(second), []string{"a", "d"}) {
		t.Fatalf("second=%v more=%v", values(second), more)
	}
}

func TestHydratedFlowStatementCountDoesNotDependOnItems(t *testing.T) {
	for _, itemCount := range []int{0, 1, 20, 100} {
		database := &countingQueryer{}
		roots := make([]model.Moin, itemCount)
		for index := range roots {
			roots[index].ID = "moin"
		}
		if err := Hydrate(t.Context(), database, roots, "viewer"); err != nil {
			t.Fatal(err)
		}
		expected := PostListSQLCount
		if itemCount == 0 {
			expected = 1
		}
		if got := 1 + database.queries; got != expected {
			t.Fatalf("items=%d statements=%d", itemCount, got)
		}
	}
}

func TestApplyDetailsPopulatesDuplicatePostPointers(t *testing.T) {
	root := model.Moin{ID: "same", Author: model.User{Email: "private@example.test", Provider: "local", Roles: []string{"member"}}}
	relatedCopy := model.Moin{ID: "same", Author: root.Author}
	initialize(&root)
	initialize(&relatedCopy)
	err := applyDetails(
		[]*model.Moin{&root, &relatedCopy},
		[]byte(`[{"id":"media_1","filename":"chart.png","altText":"성능 차트","mimeType":"image/png"}]`),
		[]byte(`[{"id":"topic_1","slug":"go","name":"Go"}]`),
		[]byte(`{"useful":3}`),
		[]byte(`["useful"]`),
		4, 2, true, true, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, post := range []*model.Moin{&root, &relatedCopy} {
		if len(post.Media) != 1 || post.Media[0].AltText != "성능 차트" || post.Signals["useful"] != 3 || post.ReplyCount != 4 || !post.Author.Following {
			t.Fatalf("duplicate target was not hydrated: %+v", post)
		}
		if post.Author.Email != "" || post.Author.Provider != "" || post.Author.Roles != nil {
			t.Fatalf("private author fields leaked: %+v", post.Author)
		}
	}
}

func values(items []Ranked[string]) []string {
	result := make([]string, len(items))
	for index := range items {
		result[index] = items[index].Value
	}
	return result
}

type countingQueryer struct{ queries int }

func (queryer *countingQueryer) Query(context.Context, string, ...any) (pgx.Rows, error) {
	queryer.queries++
	return &emptyRows{}, nil
}

type emptyRows struct{}

func (*emptyRows) Close()                                       {}
func (*emptyRows) Err() error                                   { return nil }
func (*emptyRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*emptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (*emptyRows) Next() bool                                   { return false }
func (*emptyRows) Scan(...any) error                            { return pgx.ErrNoRows }
func (*emptyRows) Values() ([]any, error)                       { return nil, pgx.ErrNoRows }
func (*emptyRows) RawValues() [][]byte                          { return nil }
func (*emptyRows) Conn() *pgx.Conn                              { return nil }

func BenchmarkPageTwoHundredCandidates(b *testing.B) {
	items := make([]Ranked[int], 200)
	for index := range items {
		items[index] = Ranked[int]{Value: index, ID: string(rune(0x1000 + index)), Score: float64(index % 13)}
	}
	for b.Loop() {
		page, _ := Page(items, nil, 30, 0)
		if len(page) != 30 {
			b.Fatal(len(page))
		}
	}
}
