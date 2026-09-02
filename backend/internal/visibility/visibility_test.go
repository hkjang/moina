package visibility

import "testing"

// The literals below are the exact predicates the read paths carried before
// they shared this package. Pinning them keeps the extraction provably
// behaviour preserving and turns any future edit into a deliberate one.
func TestMoinMatchesTheOriginalFeedPredicate(t *testing.T) {
	want := `(p.visibility='public' OR p.author_id=$1 OR p.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1 AND f.followee_id=p.author_id) OR p.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=p.moim_id AND mm.user_id=$1))`
	if got := Moin("p", "$1"); got != want {
		t.Fatalf("Moin(p,$1)=\n%s\nwant\n%s", got, want)
	}
}

func TestMoinAcceptsAColumnAsTheViewer(t *testing.T) {
	want := `(post.visibility='public' OR post.author_id=mentioned.id OR post.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=mentioned.id AND f.followee_id=post.author_id) OR post.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=post.moim_id AND mm.user_id=mentioned.id))`
	if got := Moin("post", "mentioned.id"); got != want {
		t.Fatalf("Moin(post,mentioned.id)=\n%s\nwant\n%s", got, want)
	}
}

func TestNotBlockedMatchesTheOriginalPredicate(t *testing.T) {
	want := `NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$1 AND b.blocked_id=p.author_id) OR (b.blocker_id=p.author_id AND b.blocked_id=$1))`
	if got := NotBlocked("p", "$1"); got != want {
		t.Fatalf("NotBlocked(p,$1)=\n%s\nwant\n%s", got, want)
	}
}

func TestNotBlockedBetweenTargetsAnArbitraryUserExpression(t *testing.T) {
	want := `NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=$4 AND b.blocked_id=users.id) OR (b.blocker_id=users.id AND b.blocked_id=$4))`
	if got := NotBlockedBetween("users.id", "$4"); got != want {
		t.Fatalf("NotBlockedBetween(users.id,$4)=\n%s\nwant\n%s", got, want)
	}
}

func TestNotMutedMatchesTheOriginalFlowPredicate(t *testing.T) {
	want := `NOT EXISTS(SELECT 1 FROM mutes m WHERE m.muter_id=$1 AND m.muted_id=p.author_id)`
	if got := NotMuted("p", "$1"); got != want {
		t.Fatalf("NotMuted(p,$1)=\n%s\nwant\n%s", got, want)
	}
}

func TestPublishedAndVisibleOrdersStatusBlockThenVisibility(t *testing.T) {
	got := PublishedAndVisible("p", "$1")
	if len(got) != 3 {
		t.Fatalf("PublishedAndVisible returned %d predicates, want 3", len(got))
	}
	if got[0] != `p.status='published'` {
		t.Fatalf("first predicate=%s", got[0])
	}
	if got[1] != NotBlocked("p", "$1") || got[2] != Moin("p", "$1") {
		t.Fatalf("PublishedAndVisible does not reuse NotBlocked and Moin: %#v", got)
	}
}
