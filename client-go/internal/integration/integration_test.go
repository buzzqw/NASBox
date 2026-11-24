package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T, installed ...string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	old := present
	set := map[string]bool{}
	for _, name := range installed {
		set[name] = true
	}
	present = func(binary string) bool { return set[binary] }
	t.Cleanup(func() { present = old })
}

func TestThunarInstallKeepsUserActionsAndIsIdempotent(t *testing.T) {
	setup(t, "thunar")
	original := `<?xml version="1.0" encoding="UTF-8"?>
<actions>
<action>
	<icon>utilities-terminal</icon>
	<name>Apri terminale qui</name>
	<unique-id>1234</unique-id>
	<command>exo-open --working-directory %f --launch TerminalEmulator</command>
</action>
</actions>
`
	path := thunarFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := Install("/home/u/.local/bin/nasbox-go")
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Changed) != 1 || result.Changed[0] != "Thunar" {
			t.Fatalf("result %+v", result)
		}
	}
	content, _ := os.ReadFile(path)
	text := string(content)
	if strings.Count(text, "<unique-id>"+actionID+"</unique-id>") != 1 {
		t.Fatalf("action must be present exactly once:\n%s", text)
	}
	if !strings.Contains(text, "Apri terminale qui") || !strings.Contains(text, "'/home/u/.local/bin/nasbox-go' versions %f") {
		t.Fatalf("user actions lost or bad command:\n%s", text)
	}
	if backup, _ := os.ReadFile(path + ".nasbox-backup"); string(backup) != original {
		t.Fatal("original uca.xml must be backed up")
	}
	if !Installed() {
		t.Fatal("Installed must detect the Thunar action")
	}
	if _, err := Remove(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), actionID) || !strings.Contains(string(after), "Apri terminale qui") {
		t.Fatalf("remove must drop only our action:\n%s", after)
	}
}

func TestInstallSkipsMissingFileManagers(t *testing.T) {
	setup(t, "nemo", "dolphin")
	result, err := Install("/opt/nasbox-go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Changed, ",") != "Nemo,Dolphin" {
		t.Fatalf("changed %v", result.Changed)
	}
	if _, err := os.Stat(nautilusScript()); !os.IsNotExist(err) {
		t.Fatal("no Nautilus entry without Nautilus")
	}
	content, _ := os.ReadFile(nemoAction())
	if !strings.Contains(string(content), "Exec='/opt/nasbox-go' versions %F") {
		t.Fatalf("nemo action:\n%s", content)
	}
	removed, _ := Remove()
	if len(removed.Changed) != 2 || Installed() {
		t.Fatalf("remove %+v", removed)
	}
}
