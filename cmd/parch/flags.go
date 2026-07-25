package main

import "strings"

// reorderFlags moves flag tokens ahead of positional tokens so trailing
// flags parse: Go's flag package stops at the first positional, which
// silently turned `parch mark "phrase" file.html -o out.html` into a
// search for the phrase "-o". boolFlags names the flags that take no
// value (so the token after them isn't mistaken for their value).
//
// `--` ends option parsing: everything after it is positional even if it
// starts with a dash — without this, a SEARCH tool could not search for
// "-foo". The returned slice keeps an explicit `--` ahead of the
// positionals so the flag package agrees with the reordering.
func reorderFlags(args []string, boolFlags map[string]bool) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(a, "=") || boolFlags[name] {
			continue
		}
		// Consumes the following token as its value.
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	out := append(flags, "--")
	return append(out, pos...)
}
