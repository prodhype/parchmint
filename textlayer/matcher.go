package textlayer

import (
	"fmt"
	"regexp"
	"unicode/utf16"
	"unicode/utf8"
)

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
type MatchOptions struct {
	// Regex treats the query as a Go regular expression, matched against
	// the RAW block text — unlike phrase mode there is no case/accent
	// folding and no punctuation-insensitivity (case is handled by
	// IgnoreCase). `.` does not match `\n` unless the pattern opts in
	// with `(?s)`; a `\n` inside a block is a `<br>`, not a paragraph
	// break. Matching stays block-scoped: a regex can never cross blocks.
	Regex bool

	// IgnoreCase makes a Regex query case-insensitive (compiles it with
	// `(?i)`). Phrase mode is case-insensitive already; this flag only
	// applies to Regex.
	IgnoreCase bool
}

// Compile builds the Matcher for a query under the given options.
func Compile(query string, opts MatchOptions) (Matcher, error) {
	if opts.Regex {
		pattern := query
		if opts.IgnoreCase {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("bad regex %q: %w", query, err)
		}
		return regexMatcher{re: re}, nil
	}
	return ParseQuery(query)
}

// regexMatcher matches a regular expression against raw Block.Text, one
// block at a time. Go's regexp reports UTF-8 byte offsets; a Hit must be
// UTF-16 code units — the layer's offset space — so every match location
// is converted before the Hit is built, or highlighting and geometry
// would land on the wrong characters in non-ASCII text.
type regexMatcher struct{ re *regexp.Regexp }

func (m regexMatcher) FindBlock(b *Block) []Hit {
	locs := m.re.FindAllStringIndex(b.Text, -1)
	if len(locs) == 0 {
		return nil
	}
	conv := utf16Offset(b.Text)
	var hits []Hit
	for _, loc := range locs {
		if loc[0] == loc[1] {
			continue // zero-width match: nothing to highlight
		}
		hits = append(hits, Hit{Block: b, Start: conv(loc[0]), End: conv(loc[1])})
	}
	return hits
}

// utf16Offset returns a converter from UTF-8 byte offsets in s to UTF-16
// code-unit offsets. Inputs must be non-decreasing (FindAllStringIndex
// yields matches in order), so converting every offset costs one walk
// over s in total.
func utf16Offset(s string) func(int) int {
	byteOff, u16Off := 0, 0
	return func(off int) int {
		for byteOff < off {
			r, size := utf8.DecodeRuneInString(s[byteOff:])
			byteOff += size
			if n := utf16.RuneLen(r); n > 0 {
				u16Off += n
			} else {
				u16Off++
			}
		}
		return u16Off
	}
}

// FindAll runs a matcher over every block of the layer, in block order.
func FindAll(m Matcher, layer *Layer) []Hit {
	var hits []Hit
	for i := range layer.Blocks {
		hits = append(hits, m.FindBlock(&layer.Blocks[i])...)
	}
	return hits
}
