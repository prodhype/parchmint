package main

import (
	"reflect"
	"testing"
)

func TestReorderFlags(t *testing.T) {
	bools := map[string]bool{"json": true, "q": true}
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"trailing flags move ahead of positionals",
			[]string{"phrase", "file.html", "-o", "out.html"},
			[]string{"-o", "out.html", "--", "phrase", "file.html"}},
		{"bool flag consumes no value",
			[]string{"-json", "phrase", "file.html"},
			[]string{"-json", "--", "phrase", "file.html"}},
		{"value flag consumes the next token",
			[]string{"-m", "3", "phrase"},
			[]string{"-m", "3", "--", "phrase"}},
		{"equals form consumes nothing",
			[]string{"-color=always", "phrase"},
			[]string{"-color=always", "--", "phrase"}},
		{"double-dash spelling of a bool flag",
			[]string{"--json", "phrase"},
			[]string{"--json", "--", "phrase"}},
		{"bare dash is positional (stdin)",
			[]string{"-q", "-", "x"},
			[]string{"-q", "--", "-", "x"}},
		{"-- ends options",
			[]string{"-json", "--", "-not-a-flag", "file"},
			[]string{"-json", "--", "-not-a-flag", "file"}},
		{"leading -- protects a dash query",
			[]string{"--", "-foo", "f.html"},
			[]string{"--", "-foo", "f.html"}},
		{"value flag at the end does not panic",
			[]string{"-m"},
			[]string{"-m", "--"}},
		{"empty args",
			nil,
			[]string{"--"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reorderFlags(tt.in, bools)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("reorderFlags(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidStyle(t *testing.T) {
	for _, ok := range []string{"", "bg", "underline", "box", "bold"} {
		if err := validStyle(ok); err != nil {
			t.Errorf("validStyle(%q): unexpected error %v", ok, err)
		}
	}
	if err := validStyle("blink"); err == nil {
		t.Error("validStyle(blink): expected error")
	}
}

func TestPickBool(t *testing.T) {
	cfgFalse := false
	cfgTrue := true
	tests := []struct {
		name    string
		set     map[string]bool
		flagVal bool
		cfgVal  *bool
		def     bool
		want    bool
	}{
		{"default when unset", nil, false, nil, true, true},
		{"config false overrides default", nil, true, &cfgFalse, true, false},
		{"config true overrides default", nil, false, &cfgTrue, false, true},
		{"flag overrides config", map[string]bool{"favicon": true}, true, &cfgFalse, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickBool(tt.set, "favicon", tt.flagVal, tt.cfgVal, tt.def); got != tt.want {
				t.Fatalf("pickBool() = %v, want %v", got, tt.want)
			}
		})
	}
}
