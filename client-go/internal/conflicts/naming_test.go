package conflicts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func expectedPythonToken(relative, digest string) string {
	sum := sha256.Sum256([]byte(relative + "\x00" + digest))
	return hex.EncodeToString(sum[:4])
}

func TestConflictNameMatchesPython(t *testing.T) {
	const digest = "0123456789abcdef"
	cases := []struct {
		original string
		want     string
	}{
		{"document.txt", "document (conflitto da pc2 TOKEN).txt"},
		{"README", "README (conflitto da pc2 TOKEN)"},
		{"archive.tar.gz", "archive.tar (conflitto da pc2 TOKEN).gz"},
		{".env", ".env (conflitto da pc2 TOKEN)"},
		{".config.json", ".config (conflitto da pc2 TOKEN).json"},
		{"foo.", "foo (conflitto da pc2 TOKEN)."},
		{"..foo", "..foo (conflitto da pc2 TOKEN)"},
		{"folder/a.b.c", "a.b (conflitto da pc2 TOKEN).c"},
	}
	for _, test := range cases {
		t.Run(test.original, func(t *testing.T) {
			token := expectedPythonToken(test.original, digest)
			want := strings.ReplaceAll(test.want, "TOKEN", token)
			got, err := ConflictName(test.original, "pc2", digest)
			if err != nil || got != want {
				t.Fatalf("ConflictName() = %q, %v; want %q", got, err, want)
			}
			original, ok := originalName(got)
			if !ok || original != path.Base(test.original) {
				t.Fatalf("name does not round-trip: original=%q ok=%v", original, ok)
			}
		})
	}
}

func TestConflictNameIsBoundedAndValidUTF8(t *testing.T) {
	original := strings.Repeat("文件", 100) + ".tar.gz"
	name, err := ConflictName(original, "pc2", "digest")
	if err != nil {
		t.Fatal(err)
	}
	if len(name) > MaxConflictNameBytes || len(name) >= 255 {
		t.Fatalf("name too long: %d bytes", len(name))
	}
	if !utf8.ValidString(name) {
		t.Fatalf("truncation produced invalid UTF-8: %q", name)
	}
	if !strings.HasSuffix(name, ".gz") {
		t.Fatalf("multi-extension suffix was lost: %q", name)
	}

	// Python drops an oversized suffix before truncating the stem.
	original = "a." + strings.Repeat("x", 230)
	name, err = ConflictName(original, "pc2", "digest")
	if err != nil {
		t.Fatal(err)
	}
	if len(name) > MaxConflictNameBytes || strings.HasSuffix(name, "."+strings.Repeat("x", 230)) {
		t.Fatalf("oversized suffix was not handled: %d bytes", len(name))
	}
}

func TestConflictNameSanitizesOriginAndRejectsUnsafePaths(t *testing.T) {
	name, err := ConflictName("doc.txt", "../bad ) device", "digest")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(name, "/\\") {
		t.Fatalf("origin introduced a path separator: %q", name)
	}
	parts, ok := parseConflictName(name)
	if !ok || strings.ContainsAny(parts.origin, " )") {
		t.Fatalf("origin is not parseable: %+v ok=%v", parts, ok)
	}
	for _, unsafe := range []string{"", ".", "..", "../doc.txt", "/doc.txt", "a/../../doc.txt", "bad\x00name"} {
		if _, err := ConflictName(unsafe, "pc2", "digest"); err == nil {
			t.Errorf("ConflictName(%q) accepted an unsafe path", unsafe)
		}
	}
}

func TestCreateRoundTripForExtensionlessAndMultipleExtensions(t *testing.T) {
	for _, original := range []string{"README", "archive.tar.gz", ".env", "nested/foo."} {
		t.Run(original, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, original, "payload")
			name, err := Create(root, original, "pc2", "same-digest")
			if err != nil {
				t.Fatal(err)
			}
			requireTestContent(t, root, name, "payload")
			groups, err := Scan(root, nil)
			if err != nil || len(groups) != 1 || groups[0].Original != original {
				t.Fatalf("round-trip groups = %+v, %v", groups, err)
			}
			if len(groups[0].Candidates) != 1 || groups[0].Candidates[0] != name {
				t.Fatalf("round-trip candidate = %+v, want %q", groups[0].Candidates, name)
			}
		})
	}
}

func TestCreateIsDeterministicAndAtomic(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "doc.txt", "first")
	mtime := time.Unix(1_700_000_321, 123_456_789)
	if err := os.Chtimes(filepath.Join(root, "doc.txt"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	first, err := Create(root, "doc.txt", "pc2", "digest")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(first)))
	if err != nil || info.Mode().Perm() != 0o640 || !info.ModTime().Equal(mtime) {
		t.Fatalf("conflict copy did not preserve metadata: %+v, %v", info, err)
	}
	writeTestFile(t, root, "doc.txt", "second")
	second, err := Create(root, "doc.txt", "pc2", "digest")
	if err != nil || first != second {
		t.Fatalf("retry changed destination: %q -> %q (%v)", first, second, err)
	}
	requireTestContent(t, root, second, "second")
	matches, err := filepath.Glob(filepath.Join(root, ".nasbox-conflict-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files leaked: %v, %v", matches, err)
	}
}
