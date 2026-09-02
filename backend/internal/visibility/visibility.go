// Package visibility holds the SQL predicates that decide which Moin a viewer
// may read. Flow, search, replies, a single Moin, hydration of a quoted Moin,
// mention notifications, and media access all enforce the same rules, so they
// all build their WHERE clause from here. Keeping one copy is a security
// property, not a style preference: a rule that is fixed in five places and
// missed in the sixth leaks the Moin that the sixth path serves.
package visibility

import "fmt"

// Moin reports whether viewerExpr may read the Moin aliased as postAlias.
// viewerExpr is any SQL expression naming the viewer, so callers pass either a
// placeholder such as "$1" or a column such as "mentioned.id".
//
// It deliberately says nothing about status. A draft is visible to its author
// and to nobody else, and only the caller knows whether it is reading a feed
// (published only) or a single Moin (the author's own draft included), so each
// caller adds its own status rule alongside this one.
func Moin(postAlias, viewerExpr string) string {
	return fmt.Sprintf(
		`(%[1]s.visibility='public'`+
			` OR %[1]s.author_id=%[2]s`+
			` OR %[1]s.visibility='followers' AND EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=%[2]s AND f.followee_id=%[1]s.author_id)`+
			` OR %[1]s.visibility='moim' AND EXISTS(SELECT 1 FROM moim_members mm WHERE mm.moim_id=%[1]s.moim_id AND mm.user_id=%[2]s))`,
		postAlias, viewerExpr)
}

// NotBlockedBetween excludes a pair where either side has blocked the other.
// Blocking is symmetric for reads: the blocker stops seeing the blocked user
// and stops being seen by them.
func NotBlockedBetween(userExpr, viewerExpr string) string {
	return fmt.Sprintf(
		`NOT EXISTS(SELECT 1 FROM blocks b WHERE (b.blocker_id=%[2]s AND b.blocked_id=%[1]s) OR (b.blocker_id=%[1]s AND b.blocked_id=%[2]s))`,
		userExpr, viewerExpr)
}

// NotBlocked is NotBlockedBetween against the author of postAlias.
func NotBlocked(postAlias, viewerExpr string) string {
	return NotBlockedBetween(postAlias+".author_id", viewerExpr)
}

// NotMuted excludes authors the viewer muted. Mute only quiets a Flow; unlike a
// block it is not an access rule, so a muted author's Moin still resolves
// through search, a direct link, and hydration.
func NotMuted(postAlias, viewerExpr string) string {
	return fmt.Sprintf(
		`NOT EXISTS(SELECT 1 FROM mutes m WHERE m.muter_id=%[2]s AND m.muted_id=%[1]s.author_id)`,
		postAlias, viewerExpr)
}

// PublishedAndVisible is the common feed-shaped pair: the Moin is published and
// the viewer may read it.
func PublishedAndVisible(postAlias, viewerExpr string) []string {
	return []string{
		postAlias + ".status='published'",
		NotBlocked(postAlias, viewerExpr),
		Moin(postAlias, viewerExpr),
	}
}
