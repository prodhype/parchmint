package textlayer

import "testing"

// compileStrings runs a compiled matcher against one block's text and
// returns the matched substrings.
func compileStrings(t *testing.T, query string, opts MatchOptions, text string) []string {
	t.Helper()
	m, err := Compile(query, opts)
	if err != nil {
		t.Fatalf("Compile(%q, %+v): %v", query, opts, err)
	}
	b := &Block{Text: text}
	var out []string
	for _, h := range m.FindBlock(b) {
		out = append(out, h.Text())
	}
	return out
}

func TestCompileDefaultIsPhrase(t *testing.T) {
	// Zero options must give the loose phrase matcher — "phone finds
	// iPhone" is load-bearing.
	got := compileStrings(t, "phone", MatchOptions{}, "Buy the new iPhone today")
	if len(got) != 1 || got[0] != "Phone" {
		t.Errorf("got %v, want [Phone]", got)
	}
}

func TestCompileBadQuery(t *testing.T) {
	if _, err := Compile("  !!! ", MatchOptions{}); err == nil {
		t.Error("expected error for query with no searchable tokens")
	}
}

func TestRegexMatcher(t *testing.T) {
	tests := []struct {
		name  string
		query string
		opts  MatchOptions
		text  string
		want  []string
	}{
		{"alternation", `trilob(ite|ita)s?`, MatchOptions{Regex: true},
			"fossils: trilobita, trilobites, trilobite", []string{"trilobita", "trilobites", "trilobite"}},
		{"case-sensitive by default", `Fox`, MatchOptions{Regex: true},
			"the fox and the Fox", []string{"Fox"}},
		{"ignore case", `fox`, MatchOptions{Regex: true, IgnoreCase: true},
			"the fox and the FOX", []string{"fox", "FOX"}},
		{"anchors are block anchors", `^the`, MatchOptions{Regex: true},
			"the quick the lazy", []string{"the"}},
		{"dot does not cross br newline", `quick.fox`, MatchOptions{Regex: true},
			"quick\nfox", nil},
		{"dotall opts into br newline", `(?s)quick.fox`, MatchOptions{Regex: true},
			"quick\nfox", []string{"quick\nfox"}},
		{"zero-width matches dropped", `x*`, MatchOptions{Regex: true},
			"a xx b", []string{"xx"}},
		{"bad pattern", `(`, MatchOptions{Regex: true}, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := Compile(tt.query, tt.opts)
			if err != nil {
				if tt.name == "bad pattern" {
					return
				}
				t.Fatalf("Compile(%q): %v", tt.query, err)
			}
			if tt.name == "bad pattern" {
				t.Fatal("expected compile error")
			}
			var got []string
			for _, h := range m.FindBlock(&Block{Text: tt.text}) {
				got = append(got, h.Text())
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("hit %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRegexUTF16Offsets(t *testing.T) {
	// Multibyte text before the match: é (2 UTF-8 bytes, 1 UTF-16 unit),
	// 中 (3 bytes, 1 unit), 😀 (4 bytes, 2 units — surrogate pair). Byte
	// offsets and UTF-16 offsets diverge; the Hit must be in UTF-16.
	b := &Block{Text: "é中😀 fox"}
	m, err := Compile(`f.x`, MatchOptions{Regex: true})
	if err != nil {
		t.Fatal(err)
	}
	hits := m.FindBlock(b)
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	// UTF-16: é=1, 中=1, 😀=2, space=1 → match starts at 5, ends at 8.
	if hits[0].Start != 5 || hits[0].End != 8 {
		t.Errorf("range [%d,%d), want [5,8)", hits[0].Start, hits[0].End)
	}
	if hits[0].Text() != "fox" {
		t.Errorf("text %q, want %q", hits[0].Text(), "fox")
	}
}

func TestFuzzyMatcher(t *testing.T) {
	tests := []struct {
		name  string
		query string
		dist  int
		text  string
		want  []string
	}{
		{"ocr truncation", "Music", 1, "Apple Musi on every device", []string{"Musi"}},
		{"substitution", "quick", 1, "the quack brown fox", []string{"quack"}},
		{"insertion", "brown", 1, "a browwn dog", []string{"browwn"}},
		{"deletion", "jumped", 1, "it jmped high", []string{"jmped"}},
		{"transposition costs two", "field", 2, "the feild below", []string{"feild"}},
		{"transposition beyond budget", "field", 1, "the feild below", nil},
		{"beyond budget", "quick", 1, "the qwakc brown fox", nil},
		{"short words stay exact", "cat", 2, "a cot sat", nil},
		{"short words match exactly", "cat", 2, "a cat sat", []string{"cat"}},
		{"multi-word consecutive", "quick brown", 1, "the quack browm fox", []string{"quack browm"}},
		{"multi-word one too far", "quick brown", 1, "the quack green fox", nil},
		{"case folds like phrase mode", "music", 1, "MUSI everywhere", []string{"MUSI"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compileStrings(t, tt.query, MatchOptions{Fuzzy: tt.dist}, tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("hit %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFuzzyValidation(t *testing.T) {
	if _, err := Compile("word", MatchOptions{Fuzzy: 4}); err == nil {
		t.Error("expected error for fuzzy distance beyond the cap")
	}
	if _, err := Compile("word", MatchOptions{Fuzzy: -1}); err == nil {
		t.Error("expected error for negative fuzzy distance")
	}
	if _, err := Compile("word", MatchOptions{Fuzzy: 1, Regex: true}); err == nil {
		t.Error("expected error for regex+fuzzy")
	}
}

func TestModifiers(t *testing.T) {
	tests := []struct {
		name  string
		query string
		opts  MatchOptions
		text  string
		want  []string
	}{
		// -w: whole word flips the loose substring default.
		{"whole word blocks substring", "phone", MatchOptions{WholeWord: true}, "Buy the new iPhone today", nil},
		{"whole word still matches", "phone", MatchOptions{WholeWord: true}, "an old phone here", []string{"phone"}},
		{"whole word phrase", "brown fox", MatchOptions{WholeWord: true}, "the brown foxes ran", nil},
		{"whole word wildcard spans token", "trans*ion", MatchOptions{WholeWord: true}, "the transaction posted", []string{"transaction"}},
		{"whole word wildcard partial", "rans*io", MatchOptions{WholeWord: true}, "the transaction posted", nil},
		// -s: case-sensitive, accents still fold.
		{"case-sensitive blocks fold", "Apple", MatchOptions{CaseSens: true}, "an apple a day", nil},
		{"case-sensitive matches", "Apple", MatchOptions{CaseSens: true}, "an Apple a day", []string{"Apple"}},
		{"case-sensitive keeps accent fold", "CAFE", MatchOptions{CaseSens: true}, "at the CAFÉ", []string{"CAFÉ"}},
		// -F: literal raw substring.
		{"fixed literal punctuation", "state-of-the-art", MatchOptions{Fixed: true}, "a state-of-the-art rig", []string{"state-of-the-art"}},
		{"fixed star is literal", "2*3", MatchOptions{Fixed: true}, "compute 2*3 now", []string{"2*3"}},
		{"fixed star does not bridge", "apple*card", MatchOptions{Fixed: true}, "an Apple Gift Card here", nil},
		{"fixed leading dash", "-foo", MatchOptions{Fixed: true}, "flag -foo given", []string{"-foo"}},
		{"fixed case-insensitive default", "iphone", MatchOptions{Fixed: true}, "the iPhone", []string{"iPhone"}},
		{"fixed case-sensitive", "iphone", MatchOptions{Fixed: true, CaseSens: true}, "the iPhone", nil},
		{"fixed no punctuation folding", "hello world", MatchOptions{Fixed: true}, "Hello, world!", nil},
		{"fixed whole word", "art", MatchOptions{Fixed: true, WholeWord: true}, "art of the artful", []string{"art"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compileStrings(t, tt.query, tt.opts, tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("hit %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestModifierCombos(t *testing.T) {
	bad := []MatchOptions{
		{Regex: true, Fixed: true},
		{Regex: true, Fuzzy: 1},
		{Fixed: true, Fuzzy: 1},
		{Regex: true, WholeWord: true},
		{Regex: true, CaseSens: true},
	}
	for _, opts := range bad {
		if _, err := Compile("word", opts); err == nil {
			t.Errorf("expected error for %+v", opts)
		}
	}
	// Fuzzy respects -s.
	got := compileStrings(t, "Music", MatchOptions{Fuzzy: 1, CaseSens: true}, "MUSI here and Musi there")
	if len(got) != 1 || got[0] != "Musi" {
		t.Errorf("fuzzy case-sensitive: got %v, want [Musi]", got)
	}
}

func TestScopedMatcher(t *testing.T) {
	layer := &Layer{Blocks: []Block{
		{ID: 0, Type: "h1", Text: "revenue report"},
		{ID: 1, Type: "p", Text: "revenue rose"},
		{ID: 2, Type: "td", Text: "revenue: 12M"},
		{ID: 3, Type: "img", Source: "ocr", Text: "revenue chart"},
	}}
	find := func(opts MatchOptions) []int {
		t.Helper()
		m, err := Compile("revenue", opts)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int
		for _, h := range FindAll(m, layer) {
			ids = append(ids, h.Block.ID)
		}
		return ids
	}
	eq := func(got, want []int) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	if got := find(MatchOptions{}); !eq(got, []int{0, 1, 2, 3}) {
		t.Errorf("unscoped: got %v", got)
	}
	if got := find(MatchOptions{InTypes: []string{"td", "th"}}); !eq(got, []int{2}) {
		t.Errorf("in td,th: got %v", got)
	}
	if got := find(MatchOptions{Source: "ocr"}); !eq(got, []int{3}) {
		t.Errorf("source ocr: got %v", got)
	}
	if got := find(MatchOptions{Source: "dom"}); !eq(got, []int{0, 1, 2}) {
		t.Errorf("source dom: got %v", got)
	}
	// Scoping composes with any core, e.g. regex.
	if got := find(MatchOptions{Regex: true, InTypes: []string{"h1"}}); !eq(got, []int{0}) {
		t.Errorf("regex in h1: got %v", got)
	}
	if _, err := Compile("x", MatchOptions{Source: "image"}); err == nil {
		t.Error("expected error for unknown source")
	}
}

func TestMergeTermRanges(t *testing.T) {
	b := &Block{ID: 3}
	hitsByTerm := [][]Hit{
		{{Block: b, Start: 0, End: 10}, {Block: b, Start: 30, End: 35}}, // term 0
		{{Block: b, Start: 5, End: 15}},                                 // term 1 overlaps term 0
		{{Block: b, Start: 8, End: 12}},                                 // term 2 overlaps both
	}
	got := MergeTermRanges(hitsByTerm)[3]
	want := []TermRange{
		{Start: 0, End: 5, Term: 0},   // term 0 up to where term 1 starts
		{Start: 5, End: 8, Term: 1},   // term 1 until term 2 starts
		{Start: 8, End: 12, Term: 2},  // term 2 wins the middle (last term)
		{Start: 12, End: 15, Term: 1}, // term 1 resumes
		{Start: 30, End: 35, Term: 0}, // disjoint term 0 range untouched
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("range %d: got %v, want %v", i, got[i], want[i])
		}
	}
	// Ranges must be disjoint and sorted — the DOM marker depends on it.
	for i := 1; i < len(got); i++ {
		if got[i].Start < got[i-1].End {
			t.Errorf("ranges overlap: %v then %v", got[i-1], got[i])
		}
	}
}

func TestMergeTermRangesAdjacentSameTerm(t *testing.T) {
	b := &Block{ID: 1}
	got := MergeTermRanges([][]Hit{{
		{Block: b, Start: 0, End: 5},
		{Block: b, Start: 5, End: 9},
	}})[1]
	if len(got) != 1 || got[0] != (TermRange{Start: 0, End: 9, Term: 0}) {
		t.Errorf("adjacent same-term ranges should merge: got %v", got)
	}
}

func TestFindAll(t *testing.T) {
	layer := &Layer{Blocks: []Block{
		{ID: 0, Text: "the quick brown fox"},
		{ID: 1, Text: "no such animal"},
		{ID: 2, Text: "a fox again"},
	}}
	m, err := Compile("fox", MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hits := FindAll(m, layer)
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	if hits[0].Block.ID != 0 || hits[1].Block.ID != 2 {
		t.Errorf("hits in blocks %d,%d; want 0,2", hits[0].Block.ID, hits[1].Block.ID)
	}
}
