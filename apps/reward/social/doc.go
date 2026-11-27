// Package social proves, from a network's own public data, that a member's post
// exists, is public, and carries the member's own referral link.
//
// A member pastes the address of a post. Locate says which network it is on and
// names the post once (Target). Read fetches the post from the network's public
// endpoint and reports what it states (Page). Ref says whether the post carries
// a hanzo.ai link whose ref is the member's code, after following short links.
// Recent lists a member's recent public posts by their handle on a network.
//
// Everything this package fetches goes through one guard (guard.go) that admits
// only https, port 443 and a fixed list of hosts, refuses any host that resolves
// to a non-public address, dials the address it checked, follows no redirect and
// bounds time and size. The package imports nothing outside the standard
// library.
//
// The networks, by the name Target.Network gives them:
//
//   - x, bluesky, reddit, hn, mastodon: read from the network's public API.
//   - devto: dev.to/user/slug, read from the DEV article API.
//   - medium: medium.com/@user/title-id, read from the author's RSS feed. The feed
//     holds only the latest items (about ten), so a story older than that is not
//     in it and reads as Gone, as a removed one does: the caller takes it for
//     removed. A publication's address (no @user in it) is refused.
//   - hashnode: name.hashnode.dev/slug (or hashnode.com/slug), read from the
//     page itself, which must name itself as the post (its canonical link and a
//     BlogPosting). It reads the anchors of the whole page, not only the article,
//     and a custom domain is not read; what makes a link count is Ref, which
//     wants the link on hanzo.ai with exactly the member's ref.
//   - linkedin, tiktok, youtube, threads, instagram, facebook: networks whose
//     posts public data cannot prove. Hanzo cannot read LinkedIn, YouTube, Threads,
//     Instagram or Facebook without the member's own sign-in there or a key it does
//     not hold, and TikTok's public oEmbed names neither the caption nor the
//     author, so it cannot show the member's link. They are located but never
//     fetched: the Target is marked Review, its Key is the network's own id for
//     the post, and a maintainer confirms the post. TikTok's short links
//     (vm.tiktok.com, vt.tiktok.com, tiktok.com/t/...) are refused: they name the
//     video only by a redirect, which is not followed.
//
// An address on any other site, a blog on its own domain among them, is refused.
// For an article the Links of a Page are only the ones a referral can be (on
// hanzo.ai or a listed short-link host): an article holds more links than the
// reader keeps, and the rest are of no use to Ref.
package social
