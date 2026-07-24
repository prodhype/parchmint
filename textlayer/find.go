package textlayer

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Query is a parsed phrase, matched Ctrl-F style within ONE block —
// matching never bridges blocks, and punctuation is invisible because it
// tokenizes to nothing. Loose by default:
//
//   - a query word matches anywhere INSIDE a page word: "phone" finds
//     "iPhone" and "smartphones";
//   - consecutive query words match consecutive page words;
//   - `*` bridges words: "apple*card" and "apple * card" both find
//     "Apple Gift Card" (up to maxGap words in the gap). An internal `*`
//     also keeps its within-word reading, so "apple*card" finds
//     "AppleGiftCard" too.
//
// Hits cover whole page words (word-level highlighting).
type Query struct {
	Raw      string
	patterns [][]elem // alternative readings (internal-star tokens have two)

	// Modifiers (MatchOptions; zero values = the loose default).
	wholeWord bool // fragments must equal whole tokens, not substrings
	caseSens  bool // tokens keep their case on both sides
}

// maxGap bounds how many words a `*` may bridge, so a stray star can't
// stitch a hit across half a paragraph.
const maxGap = 5

// elem is one step of a pattern: a word gap, or a fragment that one page
// word must contain (re holds the within-word reading of internal stars).
type elem struct {
	gap bool
	sub string
	re  *regexp.Regexp
}

func (e elem) matches(norm string, whole bool) bool {
	if e.re != nil {
		// Compiled anchored when the query is whole-word (parsePhrase).
		return e.re.MatchString(norm)
	}
	if whole {
		return norm == e.sub
	}
	return strings.Contains(norm, e.sub)
}

// byteRange returns where inside norm this element matched, as a byte
// range — the basis for substring-precise hits ("phone" in "iPhone"
// covers just the "phone" part). Callers only use it after matches().
func (e elem) byteRange(norm string) (int, int) {
	if e.re != nil {
		if loc := e.re.FindStringIndex(norm); loc != nil {
			return loc[0], loc[1]
		}
		return 0, len(norm)
	}
	if idx := strings.Index(norm, e.sub); idx >= 0 {
		return idx, idx + len(e.sub)
	}
	return 0, len(norm)
}

// ParseQuery normalizes and tokenizes a query with the same folding the
// block side uses, so both sides agree by construction.
func ParseQuery(raw string) (*Query, error) {
	return parsePhrase(raw, false, false)
}

// parsePhrase is ParseQuery with the -w/-s modifiers: wholeWord gates
// every fragment on full-token equality instead of substring containment
// ("phone" no longer hits "iPhone"), caseSens keeps case on both the
// query and block side of the fold.
func parsePhrase(raw string, wholeWord, caseSens bool) (*Query, error) {
	toks := tokenize(raw, true, caseSens)
	if len(toks) == 0 {
		return nil, fmt.Errorf("query %q has no searchable tokens", raw)
	}

	// Each token becomes one or two alternative element sequences.
	perToken := make([][][]elem, 0, len(toks))
	for _, t := range toks {
		parts := strings.FieldsFunc(t.Norm, func(r rune) bool { return r == '*' })
		switch {
		case len(parts) == 0: // bare `*`
			perToken = append(perToken, [][]elem{{{gap: true}}})
		case len(parts) == 1:
			// Edge stars are no-ops under substring matching ("trans*",
			// "*phone" ≡ "trans", "phone").
			perToken = append(perToken, [][]elem{{{sub: parts[0]}}})
		default:
			// Internal star(s): the within-word reading (fragments in
			// order inside ONE word) and the across-words reading.
			quoted := make([]string, len(parts))
			for i, p := range parts {
				quoted[i] = regexp.QuoteMeta(p)
			}
			pattern := strings.Join(quoted, ".*")
			if wholeWord {
				// Whole-word: the fragments must span the entire token.
				pattern = "^(?:" + pattern + ")$"
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("bad wildcard token %q: %w", t.Norm, err)
			}
			within := []elem{{re: re}}
			var across []elem
			for i, p := range parts {
				if i > 0 {
					across = append(across, elem{gap: true})
				}
				across = append(across, elem{sub: p})
			}
			perToken = append(perToken, [][]elem{within, across})
		}
	}

	// Cartesian expansion of the alternatives into whole-query patterns
	// (internal-star tokens are rare; cap keeps pathological queries sane).
	patterns := [][]elem{{}}
	for _, alts := range perToken {
		var next [][]elem
		for _, pat := range patterns {
			for _, alt := range alts {
				grown := append(append([]elem{}, pat...), alt...)
				next = append(next, grown)
				if len(next) >= 16 {
					break
				}
			}
		}
		patterns = next
	}

	q := &Query{Raw: raw, wholeWord: wholeWord, caseSens: caseSens}
	for _, pat := range patterns {
		// Gaps at the edges are meaningless; adjacent gaps collapse.
		trimmed := make([]elem, 0, len(pat))
		for _, e := range pat {
			if e.gap && (len(trimmed) == 0 || trimmed[len(trimmed)-1].gap) {
				continue
			}
			trimmed = append(trimmed, e)
		}
		for len(trimmed) > 0 && trimmed[len(trimmed)-1].gap {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if len(trimmed) > 0 {
			q.patterns = append(q.patterns, trimmed)
		}
	}
	if len(q.patterns) == 0 {
		return nil, fmt.Errorf("query %q has no searchable words", raw)
	}
	return q, nil
}

// matchPattern tries pattern pat against toks starting at token i,
// returning the index one past the last consumed token. Gaps backtrack up
// to maxGap; patterns start and end with fragments (guaranteed by
// ParseQuery), so a hit's edges are always real matched words.
func matchPattern(toks []Token, i int, pat []elem, whole bool) (int, bool) {
	if len(pat) == 0 {
		return i, true
	}
	e := pat[0]
	if e.gap {
		for skip := 0; skip <= maxGap && i+skip < len(toks); skip++ {
			if end, ok := matchPattern(toks, i+skip, pat[1:], whole); ok {
				return end, true
			}
		}
		return 0, false
	}
	if i >= len(toks) || !e.matches(toks[i].Norm, whole) {
		return 0, false
	}
	return matchPattern(toks, i+1, pat[1:], whole)
}

// Hit is one phrase match inside one block.
type Hit struct {
	Block      *Block
	Start, End int // UTF-16 range in Block.Text covering the match
}

// Text returns the matched substring of the block.
func (h Hit) Text() string { return UTF16Slice(h.Block.Text, h.Start, h.End) }

// Words returns the block words overlapping the match — the match's
// geometry.
func (h Hit) Words() []Word {
	var out []Word
	for _, w := range h.Block.Words {
		if w.Start < h.End && w.End > h.Start {
			out = append(out, w)
		}
	}
	return out
}

// Box returns the union box of the match's words (zero Box when the
// match has no measured words).
func (h Hit) Box() Box {
	words := h.Words()
	if len(words) == 0 {
		return Box{}
	}
	x1, y1 := words[0].Box[0], words[0].Box[1]
	x2, y2 := x1+words[0].Box[2], y1+words[0].Box[3]
	for _, w := range words[1:] {
		if w.Box[0] < x1 {
			x1 = w.Box[0]
		}
		if w.Box[1] < y1 {
			y1 = w.Box[1]
		}
		if w.Box[0]+w.Box[2] > x2 {
			x2 = w.Box[0] + w.Box[2]
		}
		if w.Box[1]+w.Box[3] > y2 {
			y2 = w.Box[1] + w.Box[3]
		}
	}
	return Box{x1, y1, x2 - x1, y2 - y1}
}

// Context returns the match with up to around runes of block context on
// each side, newlines flattened, with the match wrapped in open/close.
func (h Hit) Context(around int, open, close string) string {
	text := h.Block.Text
	pre := UTF16Slice(text, 0, h.Start)
	post := UTF16Slice(text, h.End, len(text)*2) // len*2 safely past the end
	preR := []rune(pre)
	postR := []rune(post)
	ellipsisPre, ellipsisPost := "", ""
	if len(preR) > around {
		preR = preR[len(preR)-around:]
		ellipsisPre = "…"
	}
	if len(postR) > around {
		postR = postR[:around]
		ellipsisPost = "…"
	}
	flat := func(s string) string { return strings.ReplaceAll(s, "\n", " ") }
	return ellipsisPre + flat(string(preR)) + open + flat(h.Text()) + close + flat(string(postR)) + ellipsisPost
}

// FindBlock returns every match of the phrase within one block (at most
// one hit per starting word). Hit edges are substring-precise: the first
// and last pattern fragments contribute only the characters they matched
// within their words ("phone" in "iPhone" starts after the "i"); interior
// words are covered whole.
func (q *Query) FindBlock(b *Block) []Hit {
	toks := tokenize(b.Text, false, q.caseSens)
	var hits []Hit
	for i := 0; i < len(toks); i++ {
		for _, pat := range q.patterns {
			end, ok := matchPattern(toks, i, pat, q.wholeWord)
			if !ok || end <= i {
				continue
			}
			a1, b1 := pat[0].byteRange(toks[i].Norm)
			start, hitEnd := toks[i].RawRange(a1, b1)
			if end-1 > i {
				a2, b2 := pat[len(pat)-1].byteRange(toks[end-1].Norm)
				_, hitEnd = toks[end-1].RawRange(a2, b2)
			}
			hits = append(hits, Hit{Block: b, Start: start, End: hitEnd})
			break
		}
	}
	return hits
}

// Find returns every match across the layer, in block order.
func (q *Query) Find(layer *Layer) []Hit {
	return FindAll(q, layer)
}

// MergeHitRanges groups hits by block id and merges overlapping/adjacent
// ranges — what a highlighter needs: overlapping matches (e.g. "a a" in
// "a a a", or several queries) must not produce nested wraps.
func MergeHitRanges(hits []Hit) map[int][][2]int {
	byBlock := map[int][][2]int{}
	for _, h := range hits {
		byBlock[h.Block.ID] = append(byBlock[h.Block.ID], [2]int{h.Start, h.End})
	}
	for id, ranges := range byBlock {
		sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
		merged := ranges[:1]
		for _, r := range ranges[1:] {
			last := &merged[len(merged)-1]
			if r[0] <= last[1] {
				if r[1] > last[1] {
					last[1] = r[1]
				}
			} else {
				merged = append(merged, r)
			}
		}
		byBlock[id] = merged
	}
	return byBlock
}
