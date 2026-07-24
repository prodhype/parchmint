package main

import (
	"flag"

	"github.com/goodblaster/parchmint/textlayer"
)

// matchFlags holds the matching-mode flags shared by the find, mark, and
// pdf subcommands, so every consumer of the textlayer matcher exposes the
// same modes with the same names.
type matchFlags struct {
	regex      bool
	ignoreCase bool
	fuzzy      int
	wholeWord  bool
	caseSens   bool
	fixed      bool
}

// registerMatchFlags adds the shared matching flags to fs.
func registerMatchFlags(fs *flag.FlagSet) *matchFlags {
	m := &matchFlags{}
	fs.BoolVar(&m.regex, "e", false, "query is a Go regular expression, matched on raw text (case-sensitive; see -i)")
	fs.BoolVar(&m.regex, "regex", false, "alias of -e")
	fs.BoolVar(&m.ignoreCase, "i", false, "case-insensitive regex (only with -e)")
	fs.IntVar(&m.fuzzy, "z", 0, "accept words within this edit distance (1-3) of each query word — for OCR'd text")
	fs.IntVar(&m.fuzzy, "fuzzy", 0, "alias of -z")
	fs.BoolVar(&m.wholeWord, "w", false, "match whole words only (\"phone\" no longer finds \"iPhone\")")
	fs.BoolVar(&m.wholeWord, "word", false, "alias of -w")
	fs.BoolVar(&m.caseSens, "s", false, "case-sensitive (accents and punctuation still fold)")
	fs.BoolVar(&m.caseSens, "case-sensitive", false, "alias of -s")
	fs.BoolVar(&m.fixed, "F", false, "literal substring of the raw text: no punctuation folding, no `*` wildcard")
	fs.BoolVar(&m.fixed, "fixed", false, "alias of -F")
	return m
}

// options assembles the MatchOptions after fs.Parse has run.
func (m *matchFlags) options() textlayer.MatchOptions {
	return textlayer.MatchOptions{
		Regex:      m.regex,
		IgnoreCase: m.ignoreCase,
		Fuzzy:      m.fuzzy,
		WholeWord:  m.wholeWord,
		CaseSens:   m.caseSens,
		Fixed:      m.fixed,
	}
}

// matchBoolFlags returns the value-less matching flag names merged with
// extra, for reorderFlags (so a positional after them isn't eaten as a
// flag value).
func matchBoolFlags(extra map[string]bool) map[string]bool {
	out := map[string]bool{
		"e": true, "regex": true, "i": true,
		"w": true, "word": true,
		"s": true, "case-sensitive": true,
		"F": true, "fixed": true,
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
