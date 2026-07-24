package textlayer

// Matcher is the one seam every consumer of matching goes through:
// `parch find`, capture-time -highlight, `parch mark`, and `parch pdf`
// all hold a Matcher and call FindBlock, so a new match mode lights up
// everywhere at once. Match logic lives here, behind this interface —
// never in cmd/parch or capture.
type Matcher interface {
	// FindBlock returns every match within one block. Matching is
	// block-scoped by construction — a block is the phrase boundary, and
	// no mode ever bridges two blocks.
	FindBlock(b *Block) []Hit
}

// MatchOptions selects and modifies the matching mode Compile builds.
// The zero value is the default phrase matcher: Ctrl-F-style, loose
// substring-in-word ("phone" finds iPhone), case/accent/punctuation-
// insensitive — see ParseQuery. New modes are opt-in; the default never
// changes.
type MatchOptions struct{}

// Compile builds the Matcher for a query under the given options.
func Compile(query string, opts MatchOptions) (Matcher, error) {
	return ParseQuery(query)
}

// FindAll runs a matcher over every block of the layer, in block order.
func FindAll(m Matcher, layer *Layer) []Hit {
	var hits []Hit
	for i := range layer.Blocks {
		hits = append(hits, m.FindBlock(&layer.Blocks[i])...)
	}
	return hits
}
