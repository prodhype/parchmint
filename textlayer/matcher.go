package textlayer

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
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

	// WholeWord (-w) requires every query word to equal a whole page
	// word — the opt-out of the deliberately loose substring default
	// ("phone" no longer hits "iPhone"). Applies to phrase and Fixed
	// modes; regex has \b.
	WholeWord bool

	// CaseSens (-s) keeps case on both sides of the fold (accents,
	// quotes, dashes, and punctuation are still folded in phrase mode).
	// Applies to phrase, Fuzzy, and Fixed modes; regex is case-sensitive
	// already.
	CaseSens bool

	// Fixed (-F) matches the query as a literal substring of the raw
	// block text: no tokenization, no punctuation-insensitivity, no `*`
	// wildcard. Case-insensitive like the default unless CaseSens.
	Fixed bool

	// InTypes limits matching to blocks of these types (p, h1…h6, li,
	// td, th, caption, pre, blockquote, img, other). Empty = every type.
	// Composes with any mode: "highlight revenue, but only in table
	// cells" is InTypes: td,th.
	InTypes []string

	// Source limits matching to "ocr" blocks (text `parch index` read
	// out of images) or "dom" blocks (page text). Empty = both.
	Source string
}

// maxFuzzy caps the edit-distance budget: beyond 2–3 nearly everything
// matches something.
const maxFuzzy = 3

// minFuzzyLen is the query-word length (in runes) below which fuzzy
// matching degrades to exact equality.
const minFuzzyLen = 4

// Compile builds the Matcher for a query under the given options: a
// mode core (phrase by default; Fixed/Fuzzy/Regex swap it), wrapped in
// a scope filter when InTypes/Source are set.
func Compile(query string, opts MatchOptions) (Matcher, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}

	var core Matcher
	switch {
	case opts.Fixed:
		core = fixedMatcher{needle: []rune(query), caseSens: opts.CaseSens, wholeWord: opts.WholeWord}
	case opts.Fuzzy != 0:
		toks := tokenize(query, false, opts.CaseSens)
		if len(toks) == 0 {
			return nil, fmt.Errorf("query %q has no searchable tokens", query)
		}
		words := make([]string, len(toks))
		for i, t := range toks {
			words[i] = t.Norm
		}
		core = fuzzyMatcher{words: words, dist: opts.Fuzzy, caseSens: opts.CaseSens}
	case opts.Regex:
		pattern := query
		if opts.IgnoreCase {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("bad regex %q: %w", query, err)
		}
		core = regexMatcher{re: re}
	default:
		q, err := parsePhrase(query, opts.WholeWord, opts.CaseSens)
		if err != nil {
			return nil, err
		}
		core = q
	}

	if len(opts.InTypes) > 0 || opts.Source != "" {
		s := scopedMatcher{core: core, source: opts.Source}
		if len(opts.InTypes) > 0 {
			s.types = make(map[string]bool, len(opts.InTypes))
			for _, t := range opts.InTypes {
				s.types[strings.TrimSpace(t)] = true
			}
		}
		return s, nil
	}
	return core, nil
}

// scopedMatcher delegates to core only for blocks that pass the type
// and source predicates — structural scoping that composes with any
// mode ("find only inside images" = Source ocr).
type scopedMatcher struct {
	core   Matcher
	types  map[string]bool // nil = every type
	source string          // "ocr", "dom", or "" for both
}

func (m scopedMatcher) FindBlock(b *Block) []Hit {
	if m.types != nil && !m.types[b.Type] {
		return nil
	}
	switch m.source {
	case "ocr":
		if b.Source != "ocr" {
			return nil
		}
	case "dom":
		// The extractor leaves Source empty for page text; anything
		// that isn't OCR is DOM.
		if b.Source == "ocr" {
			return nil
		}
	}
	return m.core.FindBlock(b)
}

// validate rejects option combinations that would silently mean
// something other than what was asked.
func (opts MatchOptions) validate() error {
	if opts.Regex && opts.Fixed {
		return fmt.Errorf("regex (-e) and fixed (-F) are mutually exclusive")
	}
	if opts.Regex && opts.Fuzzy != 0 {
		return fmt.Errorf("regex (-e) and fuzzy (-z) are mutually exclusive")
	}
	if opts.Fixed && opts.Fuzzy != 0 {
		return fmt.Errorf("fixed (-F) and fuzzy (-z) are mutually exclusive")
	}
	if opts.Regex && opts.WholeWord {
		return fmt.Errorf("whole-word (-w) does not apply to regex; use \\b in the pattern")
	}
	if opts.Regex && opts.CaseSens {
		return fmt.Errorf("-s does not apply to regex, which is case-sensitive unless -i")
	}
	if opts.Fuzzy != 0 && (opts.Fuzzy < 0 || opts.Fuzzy > maxFuzzy) {
		return fmt.Errorf("fuzzy distance must be between 1 and %d, got %d", maxFuzzy, opts.Fuzzy)
	}
	switch opts.Source {
	case "", "ocr", "dom":
	default:
		return fmt.Errorf("source must be ocr or dom, got %q", opts.Source)
	}
	return nil
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
	words    []string
	dist     int
	caseSens bool
}

func (m fuzzyMatcher) FindBlock(b *Block) []Hit {
	toks := tokenize(b.Text, false, m.caseSens)
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

// fixedMatcher matches the query as a literal substring of raw
// Block.Text — what grep -F is to grep. No tokenization means no
// punctuation-insensitivity and no `*` wildcard; matching "-foo" or
// "C++" works exactly as typed. Comparison is rune-by-rune (simple
// ToLower when case-insensitive), so offsets never shift under folding;
// found matches are non-overlapping, like grep.
type fixedMatcher struct {
	needle    []rune
	caseSens  bool
	wholeWord bool
}

func (m fixedMatcher) FindBlock(b *Block) []Hit {
	if len(m.needle) == 0 {
		return nil
	}
	text := []rune(b.Text)
	if len(text) < len(m.needle) {
		return nil
	}
	// Cumulative UTF-16 offset of each rune — Hits live in UTF-16.
	offs := make([]int, len(text)+1)
	for i, r := range text {
		n := utf16.RuneLen(r)
		if n < 0 {
			n = 1
		}
		offs[i+1] = offs[i] + n
	}
	eq := func(a, b rune) bool {
		if m.caseSens {
			return a == b
		}
		return a == b || unicode.ToLower(a) == unicode.ToLower(b)
	}
	isWordRune := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

	var hits []Hit
	for i := 0; i+len(m.needle) <= len(text); {
		ok := true
		for j, qr := range m.needle {
			if !eq(text[i+j], qr) {
				ok = false
				break
			}
		}
		if ok && m.wholeWord {
			end := i + len(m.needle)
			if (i > 0 && isWordRune(text[i-1])) || (end < len(text) && isWordRune(text[end])) {
				ok = false
			}
		}
		if ok {
			hits = append(hits, Hit{Block: b, Start: offs[i], End: offs[i+len(m.needle)]})
			i += len(m.needle)
		} else {
			i++
		}
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
