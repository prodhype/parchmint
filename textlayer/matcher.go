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

	// Fuzzy accepts page words within this Levenshtein distance of each
	// query word (0 = off, max 3). Matching is whole-token: each query
	// word must fuzzy-equal one page word, consecutively — the recall
	// knob for OCR'd image text, where "Music" comes out "Musi" or
	// "rn" reads as "m". Words shorter than minFuzzyLen never fuzz (too
	// many false hits); they must match exactly.
	Fuzzy int
}

// maxFuzzy caps the edit-distance budget: beyond 2–3 nearly everything
// matches something.
const maxFuzzy = 3

// minFuzzyLen is the query-word length (in runes) below which fuzzy
// matching degrades to exact equality.
const minFuzzyLen = 4

// Compile builds the Matcher for a query under the given options.
func Compile(query string, opts MatchOptions) (Matcher, error) {
	if opts.Fuzzy != 0 {
		if opts.Regex {
			return nil, fmt.Errorf("regex and fuzzy matching are mutually exclusive")
		}
		if opts.Fuzzy < 0 || opts.Fuzzy > maxFuzzy {
			return nil, fmt.Errorf("fuzzy distance must be between 1 and %d, got %d", maxFuzzy, opts.Fuzzy)
		}
		toks := Tokenize(query, false)
		if len(toks) == 0 {
			return nil, fmt.Errorf("query %q has no searchable tokens", query)
		}
		words := make([]string, len(toks))
		for i, t := range toks {
			words[i] = t.Norm
		}
		return fuzzyMatcher{words: words, dist: opts.Fuzzy}, nil
	}
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

// fuzzyMatcher matches each (normalized) query word against consecutive
// (normalized) page tokens within a Levenshtein budget. Deliberately
// token-scoped — never fuzzy across whole blocks — so cost stays linear
// in tokens and hits stay explainable. Hits cover whole page words:
// there is no meaningful sub-word range when the word only nearly
// matched.
type fuzzyMatcher struct {
	words []string
	dist  int
}

func (m fuzzyMatcher) FindBlock(b *Block) []Hit {
	toks := Tokenize(b.Text, false)
	var hits []Hit
	for i := 0; i+len(m.words) <= len(toks); i++ {
		ok := true
		for j, qw := range m.words {
			if !fuzzyEqual(toks[i+j].Norm, qw, m.dist) {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, Hit{Block: b, Start: toks[i].Start, End: toks[i+len(m.words)-1].End})
		}
	}
	return hits
}

// fuzzyEqual reports whether page is within dist edits of query — exact
// equality when the query word is too short to fuzz safely.
func fuzzyEqual(page, query string, dist int) bool {
	q := []rune(query)
	if len(q) < minFuzzyLen {
		return page == query
	}
	p := []rune(page)
	return withinLevenshtein(p, q, dist)
}

// withinLevenshtein reports edit distance ≤ max, with the standard cheap
// exits: the length difference bounds the distance from below, and a DP
// row whose minimum already exceeds max can never recover.
func withinLevenshtein(a, b []rune, max int) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > max {
		return false
	}
	prev := make([]int, len(a)+1)
	cur := make([]int, len(a)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(b); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(a); j++ {
			cost := 1
			if a[j-1] == b[i-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if cur[j] < rowMin {
				rowMin = cur[j]
			}
		}
		if rowMin > max {
			return false
		}
		prev, cur = cur, prev
	}
	return prev[len(a)] <= max
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
