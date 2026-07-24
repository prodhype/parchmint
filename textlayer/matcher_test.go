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
