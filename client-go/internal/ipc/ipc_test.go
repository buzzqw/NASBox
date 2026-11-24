package ipc

import (
	"errors"
	"testing"
)

func TestServeAndSend(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if _, err := Send("show", ""); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("without a server Send must report ErrNotRunning, got %v", err)
	}
	var got []string
	stop, err := Serve(func(command, argument string) string {
		got = append(got, command+"|"+argument)
		return "ok " + argument
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	reply, err := Send("versions", "Documenti/a b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "ok Documenti/a b.txt" || len(got) != 1 || got[0] != "versions|Documenti/a b.txt" {
		t.Fatalf("reply %q, handled %v", reply, got)
	}
}
