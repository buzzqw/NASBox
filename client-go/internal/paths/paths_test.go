package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRelativeRejectsTraversalAndInternals(t *testing.T) {
	for _, value := range []string{"", "/etc/passwd", "../x", "a/../../x", ".sync-trash/x", "@eaDir/x"} {
		if err := ValidateRelative(value); err == nil {
			t.Fatalf("accepted unsafe path %q", value)
		}
	}
	if err := ValidateRelative("cartella/file.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestSafeDestinationRejectsSymlinkParent(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeDestination(root, "link/file.txt"); err == nil {
		t.Fatal("symlink parent must be rejected")
	}
}
