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
}

// registerMatchFlags adds the shared matching flags to fs.
func registerMatchFlags(fs *flag.FlagSet) *matchFlags {
	m := &matchFlags{}
	fs.BoolVar(&m.regex, "e", false, "query is a Go regular expression, matched on raw text (case-sensitive; see -i)")
	fs.BoolVar(&m.regex, "regex", false, "alias of -e")
	fs.BoolVar(&m.ignoreCase, "i", false, "case-insensitive regex (only with -e)")
	fs.IntVar(&m.fuzzy, "z", 0, "accept words within this edit distance (1-3) of each query word — for OCR'd text")
	fs.IntVar(&m.fuzzy, "fuzzy", 0, "alias of -z")
	return m
}

// options assembles the MatchOptions after fs.Parse has run.
func (m *matchFlags) options() textlayer.MatchOptions {
	return textlayer.MatchOptions{
		Regex:      m.regex,
		IgnoreCase: m.ignoreCase,
		Fuzzy:      m.fuzzy,
	}
}

// matchBoolFlags returns the value-less matching flag names merged with
// extra, for reorderFlags (so a positional after them isn't eaten as a
// flag value).
func matchBoolFlags(extra map[string]bool) map[string]bool {
	out := map[string]bool{"e": true, "regex": true, "i": true}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
