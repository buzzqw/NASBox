package conflicts

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func makeFIFO(path string) error {
	return syscall.Mkfifo(path, 0o600)
}

func requireMissing(t *testing.T, root, relative string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s still exists (err=%v)", relative, err)
	}
}

func trashFileCount(t *testing.T) int {
	t.Helper()
	trash, err := localTrashDir()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	err = filepath.WalkDir(trash, func(_ string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			count++
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return count
}

func TestResolveCandidateIsIdempotent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	writeTestFile(t, root, "doc.txt", "original")
	first := "doc (conflitto da pc2 11111111).txt"
	second := "doc (conflitto da pc3 22222222).txt"
	writeTestFile(t, root, first, "chosen")
	writeTestFile(t, root, second, "loser")
	groups, err := Scan(root, nil)
	if err != nil || len(groups) != 1 {
		t.Fatalf("Scan = %+v, %v", groups, err)
	}
	group := groups[0]
	if err := Resolve(root, group, first); err != nil {
		t.Fatal(err)
	}
	requireTestContent(t, root, "doc.txt", "chosen")
	requireMissing(t, root, first)
	requireMissing(t, root, second)
	if count := trashFileCount(t); count != 2 {
		t.Fatalf("trash contains %d files, want original and loser", count)
	}
	if err := Resolve(root, group, first); err != nil {
		t.Fatalf("repeated Resolve failed: %v", err)
	}
}

func TestResolveOriginalIsIdempotent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	writeTestFile(t, root, "doc.txt", "original")
	candidate := "doc (conflitto da pc2 abcdef12).txt"
	writeTestFile(t, root, candidate, "alternative")
	group := Group{Original: "doc.txt", Candidates: []string{candidate}}
	if err := Resolve(root, group, "doc.txt"); err != nil {
		t.Fatal(err)
	}
	requireTestContent(t, root, "doc.txt", "original")
	requireMissing(t, root, candidate)
	if err := Resolve(root, group, "doc.txt"); err != nil {
		t.Fatalf("repeated Resolve failed: %v", err)
	}
}

func TestResolveCandidateWhenOriginalIsMissing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	candidate := "doc.txt (conflitto da pc2 abcdef12).txt"
	writeTestFile(t, root, candidate, "alternative")
	group := Group{Original: "doc.txt", Candidates: []string{candidate}}
	if err := Resolve(root, group, candidate); err != nil {
		t.Fatal(err)
	}
	requireTestContent(t, root, "doc.txt", "alternative")
	requireMissing(t, root, candidate)
	if err := Resolve(root, group, candidate); err != nil {
		t.Fatalf("repeated Resolve failed: %v", err)
	}
}

func TestDiscardIsIdempotentAndKeepsOriginal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	writeTestFile(t, root, "doc.txt", "original")
	first := "doc (conflitto da pc2 11111111).txt"
	second := "doc (conflitto da pc3 22222222).txt"
	writeTestFile(t, root, first, "one")
	writeTestFile(t, root, second, "two")
	group := Group{Original: "doc.txt", Candidates: []string{first, second, "doc (conflitto da gone 33333333).txt"}}
	if err := Discard(root, group); err != nil {
		t.Fatal(err)
	}
	requireTestContent(t, root, "doc.txt", "original")
	requireMissing(t, root, first)
	requireMissing(t, root, second)
	if count := trashFileCount(t); count != 2 {
		t.Fatalf("Discard trashed %d files, want 2", count)
	}
	if err := Discard(root, group); err != nil {
		t.Fatalf("repeated Discard failed: %v", err)
	}
}

func TestKeepBothUsesPythonNameAndIsIdempotent(t *testing.T) {
	for _, test := range []struct {
		original  string
		candidate string
		want      string
	}{
		{"README", "README (conflitto da pc2 abcdef12)", "README (copia archiviata pc2)"},
		{"archive.tar.gz", "archive.tar (conflitto da pc2 abcdef12).gz", "archive.tar (copia archiviata pc2).gz"},
		{".env", ".env (conflitto da pc2 abcdef12)", ".env (copia archiviata pc2)"},
	} {
		t.Run(test.original, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, test.original, "original")
			writeTestFile(t, root, test.candidate, "alternative")
			group := Group{Original: test.original, Candidates: []string{test.candidate}}
			kept, err := KeepBoth(root, group, test.candidate)
			if err != nil || kept != test.want {
				t.Fatalf("KeepBoth = %q, %v; want %q", kept, err, test.want)
			}
			requireTestContent(t, root, test.original, "original")
			requireTestContent(t, root, kept, "alternative")
			requireMissing(t, root, test.candidate)
			again, err := KeepBoth(root, group, test.candidate)
			if err != nil || again != kept {
				t.Fatalf("repeated KeepBoth = %q, %v; want %q", again, err, kept)
			}
		})
	}
}

func TestKeepBothCollisionUsesPythonCounterAndStableRetry(t *testing.T) {
	root := t.TempDir()
	original := "doc.tar.gz"
	candidate := "doc.tar (conflitto da pc2 abcdef12).gz"
	occupied := "doc.tar (copia archiviata pc2).gz"
	writeTestFile(t, root, original, "original")
	writeTestFile(t, root, candidate, "alternative")
	writeTestFile(t, root, occupied, "occupied")
	group := Group{Original: original, Candidates: []string{candidate}}
	kept, err := KeepBoth(root, group, candidate)
	want := "doc.tar (copia archiviata pc2 2).gz"
	if err != nil || kept != want {
		t.Fatalf("KeepBoth = %q, %v; want %q", kept, err, want)
	}
	requireTestContent(t, root, occupied, "occupied")
	requireTestContent(t, root, kept, "alternative")
	again, err := KeepBoth(root, group, candidate)
	if err != nil || again != kept {
		t.Fatalf("collision retry = %q, %v; want %q", again, err, kept)
	}
}

func TestResolutionRejectsUnsafePathsAndSpecialFiles(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tests := []struct {
		name  string
		setup func(t *testing.T, root, outside string) Group
	}{
		{
			name: "parent traversal",
			setup: func(t *testing.T, root, outside string) Group {
				relative, err := filepath.Rel(root, outside)
				if err != nil {
					t.Fatal(err)
				}
				return Group{Original: "doc.txt", Candidates: []string{relative}}
			},
		},
		{
			name: "absolute candidate",
			setup: func(t *testing.T, root, outside string) Group {
				return Group{Original: "doc.txt", Candidates: []string{outside}}
			},
		},
		{
			name: "different directory",
			setup: func(t *testing.T, root, outside string) Group {
				writeTestFile(t, root, "other/doc (conflitto da pc2 abc).txt", "inside")
				return Group{Original: "doc.txt", Candidates: []string{"other/doc (conflitto da pc2 abc).txt"}}
			},
		},
		{
			name: "symlink candidate",
			setup: func(t *testing.T, root, outside string) Group {
				candidate := "doc (conflitto da pc2 abcdef12).txt"
				if err := os.Symlink(outside, filepath.Join(root, candidate)); err != nil {
					t.Fatal(err)
				}
				return Group{Original: "doc.txt", Candidates: []string{candidate}}
			},
		},
		{
			name: "symlink parent",
			setup: func(t *testing.T, root, outside string) Group {
				link := filepath.Join(root, "linked")
				if err := os.Symlink(filepath.Dir(outside), link); err != nil {
					t.Fatal(err)
				}
				return Group{Original: "linked/doc.txt", Candidates: []string{"linked/doc (conflitto da pc2 abc).txt"}}
			},
		},
		{
			name: "fifo candidate",
			setup: func(t *testing.T, root, outside string) Group {
				candidate := "doc (conflitto da pc2 abcdef12).txt"
				if err := makeFIFO(filepath.Join(root, candidate)); err != nil {
					t.Skipf("FIFO unavailable: %v", err)
				}
				return Group{Original: "doc.txt", Candidates: []string{candidate}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			outsideDir := t.TempDir()
			outside := filepath.Join(outsideDir, "outside.txt")
			writeTestFile(t, outside, ".", "outside")
			writeTestFile(t, root, "doc.txt", "original")
			group := test.setup(t, root, outside)
			candidate := group.Candidates[0]
			if err := Resolve(root, group, candidate); err == nil {
				t.Error("Resolve accepted an unsafe/special candidate")
			}
			if err := Discard(root, group); err == nil {
				t.Error("Discard accepted an unsafe/special candidate")
			}
			if _, err := KeepBoth(root, group, candidate); err == nil {
				t.Error("KeepBoth accepted an unsafe/special candidate")
			}
			requireTestContent(t, outsideDir, "outside.txt", "outside")
			requireTestContent(t, root, "doc.txt", "original")
		})
	}
}

func TestResolutionValidatesWholeGroupBeforeMovingFiles(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	writeTestFile(t, root, "doc.txt", "original")
	valid := "doc (conflitto da pc2 abcdef12).txt"
	writeTestFile(t, root, valid, "alternative")
	group := Group{Original: "doc.txt", Candidates: []string{valid, "../outside.txt"}}
	if err := Resolve(root, group, "doc.txt"); err == nil {
		t.Fatal("Resolve did not validate every candidate")
	}
	if err := Discard(root, group); err == nil {
		t.Fatal("Discard did not validate every candidate")
	}
	requireTestContent(t, root, valid, "alternative")
}

func TestResolveRetriesPartialPromotionUsingListMetadata(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	writeTestFile(t, root, "doc.txt", "original")
	chosen := "doc (conflitto da pc2 11111111).txt"
	loser := "doc (conflitto da pc3 22222222).txt"
	writeTestFile(t, root, chosen, "chosen")
	writeTestFile(t, root, loser, "loser")
	groups, err := List(root, nil)
	if err != nil || len(groups) != 1 {
		t.Fatalf("List = %+v, %v", groups, err)
	}
	// Simulate a process restart after promotion but before losing copies were
	// moved to history.
	if err := os.Rename(filepath.Join(root, chosen), filepath.Join(root, "doc.txt")); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(root, groups[0], chosen); err != nil {
		t.Fatalf("partial resolution retry failed: %v", err)
	}
	requireTestContent(t, root, "doc.txt", "chosen")
	requireMissing(t, root, loser)
}

func TestKeepBothCollisionRetryRecoversFromListMetadata(t *testing.T) {
	root := t.TempDir()
	original := "doc.tar.gz"
	candidate := "doc.tar (conflitto da pc2 abcdef12).gz"
	occupied := "doc.tar (copia archiviata pc2).gz"
	writeTestFile(t, root, original, "original")
	writeTestFile(t, root, candidate, "alternative")
	writeTestFile(t, root, occupied, "occupied")
	groups, err := List(root, nil)
	if err != nil || len(groups) != 1 {
		t.Fatalf("List = %+v, %v", groups, err)
	}
	kept, err := KeepBoth(root, groups[0], candidate)
	if err != nil {
		t.Fatal(err)
	}
	keptCopies.Delete(keptCopyKey(rootIdentity(root), candidate))
	again, err := KeepBoth(root, groups[0], candidate)
	if err != nil || again != kept {
		t.Fatalf("restart retry = %q, %v; want %q", again, err, kept)
	}
}

func TestResolutionRejectsUnrelatedChosenPath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "doc.txt", "original")
	candidate := "doc (conflitto da pc2 abcdef12).txt"
	writeTestFile(t, root, candidate, "alternative")
	group := Group{Original: "doc.txt", Candidates: []string{candidate}}
	if err := Resolve(root, group, "other.txt"); err == nil {
		t.Fatal("Resolve accepted a path outside the group")
	}
	if _, err := KeepBoth(root, group, "doc.txt"); err == nil {
		t.Fatal("KeepBoth accepted the original")
	}
	if _, err := KeepBoth(root, group, "../other.txt"); err == nil {
		t.Fatal("KeepBoth accepted an unsafe path")
	}
	if !reflect.DeepEqual(group.Candidates, []string{candidate}) {
		t.Fatalf("group was mutated: %+v", group)
	}
}
