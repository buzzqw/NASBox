package main

import (
	"reflect"
	"testing"
)

func TestExtractGhostFlag(t *testing.T) {
	cases := []struct {
		args  []string
		want  []string
		ghost bool
	}{
		{[]string{"nasbox", "gui"}, []string{"nasbox", "gui"}, false},
		{[]string{"nasbox", "-ghost", "gui"}, []string{"nasbox", "gui"}, true},
		{[]string{"nasbox", "gui", "--ghost"}, []string{"nasbox", "gui"}, true},
		{[]string{"nasbox", "browse", "--config", "/tmp/x.json", "-ghost"},
			[]string{"nasbox", "browse", "--config", "/tmp/x.json"}, true},
	}
	for _, tc := range cases {
		ghostFlag = false
		got := extractGhostFlag(append([]string{}, tc.args...))
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("args %v: got %v, want %v", tc.args, got, tc.want)
		}
		if ghostFlag != tc.ghost {
			t.Fatalf("args %v: ghost=%v, want %v", tc.args, ghostFlag, tc.ghost)
		}
	}
	ghostFlag = false
}

func TestSplitConfigFlagAnywhere(t *testing.T) {
	path, rest := splitConfigFlag([]string{"upload", "file.txt", "dest.txt", "--config", "/tmp/ghost.json"})
	if path != "/tmp/ghost.json" {
		t.Fatalf("config path: %q", path)
	}
	if !reflect.DeepEqual(rest, []string{"upload", "file.txt", "dest.txt"}) {
		t.Fatalf("rest: %v", rest)
	}
	path, rest = splitConfigFlag([]string{"browse", "--config=/tmp/a.json", "RPG"})
	if path != "/tmp/a.json" || !reflect.DeepEqual(rest, []string{"browse", "RPG"}) {
		t.Fatalf("inline config: %q %v", path, rest)
	}
}
