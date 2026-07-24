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
