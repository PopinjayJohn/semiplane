package ext

// Embed returns the definition for `![[Page]]`.
//
// Same grammar as a wikilink, one `!` in front, and a different output element:
// an embed is a *slot* for another page's rendered content, and a link is a pointer
// to it. The distinction is not cosmetic — it is the difference between "you can
// click through" and "this page's HTML now contains the other page's HTML" — and
// the second is what ADR 0017 rules out for a cross-campaign target.
//
// The slot is empty, and that is the point:
//
//   - An empty `<span>` is identical for every viewer, which is what S-5.1's
//     permission-neutrality by construction requires and what ADR 0017's
//     byte-identity test asserts. A slot carrying the target's title would be a
//     lookup at render time, and a lookup is a read of the other page.
//   - C4 (`internal/content/links.go`) fills it: it resolves the reference, renders
//     the target, and substitutes the result — or, for a reference that resolves
//     outside this campaign, refuses it outright. **Cross-campaign embeds are
//     forbidden**, and this package deliberately does not check for one: resolution
//     is C4's, it is where the campaign boundary is known, and a check here would
//     be a second answer to a question with a security consequence.
//   - A reference that never resolves leaves an empty slot rather than a
//     placeholder saying so. The empty slot is the permission-neutral answer, and
//     C4's broken-link report (S-5.5) is where a GM is told, at index time, which
//     reference it was.
//
// `![[image.png]]` uses the same grammar and lands in the same slot. Whether the
// target is a page or an asset is a resolution question, so it is C4's, and the
// `<img>` it substitutes is C4's to emit.
func Embed() Definition {
	return Definition{
		Kind:    KindEmbed,
		Trigger: bang,
		Write:   Slot("span"),
	}
}
