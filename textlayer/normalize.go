package textlayer

import (
	"strings"
	"unicode"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"
)

// NormVersion identifies the normalizer. Archives store raw text; all
// folding happens query-side, so bumping this (with the code) makes every
// existing archive smarter without recapture. Consumers that cache
// normalized forms (a catalog index) must record it and rebuild when it
// changes.
const NormVersion = "parchmint-norm/1"

// Token is one matchable unit of a block: its normalized form plus the
// UTF-16 range of the original in Block.Text. Map carries per-BYTE
// provenance of Norm — each normalized byte's source rune as a raw UTF-16
// [start, end) — so a substring match inside a word ("phone" in
// "iPhone") maps back to exactly the matched characters, not the whole
// word, even across folds that change length (accents, ligatures,
// dropped soft hyphens).
type Token struct {
	Norm       string
	Start, End int
	Map        [][2]int
}

// RawRange maps the byte range [a, b) of Norm back to UTF-16 offsets in
// the source text.
func (t Token) RawRange(a, b int) (int, int) {
	if len(t.Map) == 0 || a < 0 || b > len(t.Map) || a >= b {
		return t.Start, t.End
	}
	return t.Map[a][0], t.Map[b-1][1]
}

// foldRuneOpt normalizes one rune: quote/dash unification, invisible-char
// removal, NFKD decomposition with combining marks stripped (café→cafe),
// lowercasing. Returns the folded runes (possibly none). caseSens skips
// only the lowercasing — a case-sensitive query (-s) still folds accents,
// quotes, dashes, and invisible characters.
func foldRuneOpt(r rune, caseSens bool) []rune {
	switch r {
	case '\u00AD', '\u200B', '\u200C', '\u200D', '\uFEFF': // soft hyphen, zero-widths, BOM
		return nil
	case '\u2018', '\u2019', '\u02BC': // curly/modifier apostrophes
		return []rune{'\''}
	case '\u201C', '\u201D': // curly double quotes
		return []rune{'"'}
	case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2212': // hyphens/dashes/minus
		return []rune{'-'}
	}
	out := make([]rune, 0, 2)
	for _, d := range norm.NFKD.String(string(r)) {
		if unicode.Is(unicode.Mn, d) {
			continue
		}
		if !caseSens {
			d = unicode.ToLower(d)
		}
		out = append(out, d)
	}
	return out
}

// isCJK reports scripts written without word spaces; each such rune is
// its own token, so phrase queries work character-by-character without a
// query-time segmenter.
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// tokenClass: 0 separator, 1 word rune, 2 CJK (single-rune token).
// keepStar treats '*' as a word rune (query parsing only).
func tokenClass(r rune, keepStar bool) int {
	switch {
	case isCJK(r):
		return 2
	case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'':
		return 1
	case keepStar && r == '*':
		return 1
	default:
		// Hyphens split ("state-of-the-art" → 4 tokens) so hyphenation
		// differences between query and page don't block matches.
		return 0
	}
}

// Tokenize breaks raw text into normalized tokens with UTF-16 ranges back
// into the original, including per-byte provenance (Token.Map). The same
// function serves blocks and (with keepStar) queries, so both sides fold
// identically by construction.
func Tokenize(text string, keepStar bool) []Token {
	return tokenize(text, keepStar, false)
}

// tokenize is Tokenize with the case fold optional (caseSens skips only
// the lowercasing; every other fold still applies).
func tokenize(text string, keepStar, caseSens bool) []Token {
	var tokens []Token
	var cur strings.Builder
	var curMap [][2]int // one entry per byte of cur: source rune's raw UTF-16 range
	pos := 0            // UTF-16 position in the original text

	flush := func() {
		s := cur.String()
		cur.Reset()
		m := curMap
		curMap = nil
		// Trim edge apostrophes ('tis, dogs') — with their provenance.
		lo, hi := 0, len(s)
		for lo < hi && s[lo] == '\'' {
			lo++
		}
		for hi > lo && s[hi-1] == '\'' {
			hi--
		}
		if hi > lo {
			m = m[lo:hi]
			tokens = append(tokens, Token{Norm: s[lo:hi], Start: m[0][0], End: m[len(m)-1][1], Map: m})
		}
	}

	for _, r := range text {
		width := utf16.RuneLen(r)
		if width < 0 {
			width = 1
		}
		folded := foldRuneOpt(r, caseSens)

		switch {
		case len(folded) == 0:
			// Invisible char (soft hyphen, zero-width): bridges a word —
			// neither extends nor breaks the current token.
		case tokenClass(folded[0], keepStar) == 2:
			flush()
			norm := string(folded)
			m := make([][2]int, len(norm))
			for i := range m {
				m[i] = [2]int{pos, pos + width}
			}
			tokens = append(tokens, Token{Norm: norm, Start: pos, End: pos + width, Map: m})
		case tokenClass(folded[0], keepStar) == 1:
			// Classify on the FOLDED rune so a curly apostrophe (folded
			// to ') stays inside "don’t" instead of splitting it.
			for _, f := range folded {
				n, _ := cur.WriteRune(f)
				for i := 0; i < n; i++ {
					curMap = append(curMap, [2]int{pos, pos + width})
				}
			}
		default:
			flush()
		}
		pos += width
	}
	flush()
	return tokens
}
