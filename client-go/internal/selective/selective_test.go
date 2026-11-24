package selective

import (
	"os"
	"path/filepath"
	"testing"

	"nasbox-go/internal/config"
)

func TestListAndSetExcluded(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"keep", "skip/inner"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	dirs := ListDirectories(root, 100)
	found := false
	for _, dir := range dirs {
		if dir == "skip/inner" {
			found = true
		}
	}
	if !found {
		t.Fatalf("subdirectory missing: %v", dirs)
	}
	cfg := config.New("")
	SetExcluded(cfg, []string{"skip"})
	if !Excluded(cfg, "skip/inner") || Excluded(cfg, "keep") {
		t.Fatal("selective exclusion wrong")
	}
}
