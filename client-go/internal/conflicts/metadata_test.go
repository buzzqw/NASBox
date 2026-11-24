package conflicts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func digestForTest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestListGroupsIncludesOriginalAndCandidateMetadata(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "doc.txt", "original")
	writeTestFile(t, root, "archive.tar.gz", "archive")
	first := "doc (conflitto da pc1 11111111).txt"
	second := "doc (conflitto da pc2 22222222).txt"
	archiveCopy := "archive.tar (conflitto da pc3 33333333).gz"
	writeTestFile(t, root, first, "candidate-one")
	writeTestFile(t, root, second, "candidate-two")
	writeTestFile(t, root, archiveCopy, "archive-copy")

	mtime := time.Unix(1_700_000_123, 456_789_123)
	if err := os.Chtimes(filepath.Join(root, "doc.txt"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	for _, internal := range []string{".sync-partial", ".sync-trash", ".nasbox-root"} {
		writeTestFile(t, root, internal+"/doc (conflitto da hidden 44444444).txt", "hidden")
	}
	writeTestFile(t, root, "skip (conflitto da pc4 55555555).txt", "excluded")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeTestFile(t, outside, ".", "outside")
	if err := os.Symlink(outside, filepath.Join(root, "doc (conflitto da linked 66666666).txt")); err != nil {
		t.Fatal(err)
	}

	excluded := func(relative string) bool { return relative == "skip (conflitto da pc4 55555555).txt" }
	groups, err := List(root, excluded)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("List returned %d groups: %+v", len(groups), groups)
	}
	if groups[0].Original != "archive.tar.gz" || groups[1].Original != "doc.txt" {
		t.Fatalf("groups are not sorted by original: %+v", groups)
	}
	archive := groups[0]
	if archive.GroupID == "" || len(archive.GroupID) != 24 {
		t.Fatalf("bad group id: %q", archive.GroupID)
	}
	if !archive.OriginalInfo.Exists || archive.OriginalInfo.Digest != digestForTest("archive") ||
		archive.OriginalInfo.Size != int64(len("archive")) || archive.OriginalInfo.Path != archive.Original {
		t.Fatalf("bad original metadata: %+v", archive.OriginalInfo)
	}
	if len(archive.Members) != 1 || archive.Members[0].Origin != "pc3" ||
		archive.Members[0].Digest != digestForTest("archive-copy") || !archive.Members[0].Exists {
		t.Fatalf("bad archive candidate metadata: %+v", archive.Members)
	}

	doc := groups[1]
	if !doc.OriginalInfo.Exists || doc.OriginalInfo.Digest != digestForTest("original") ||
		doc.OriginalInfo.Size != 8 || doc.OriginalInfo.MTimeNS != mtime.UnixNano() ||
		!doc.OriginalInfo.MTime.Equal(mtime) {
		t.Fatalf("bad doc original metadata: %+v", doc.OriginalInfo)
	}
	wantPaths := []string{first, second}
	wantOrigins := []string{"pc1", "pc2"}
	wantDigests := []string{digestForTest("candidate-one"), digestForTest("candidate-two")}
	if !reflect.DeepEqual(doc.Candidates, wantPaths) || len(doc.Members) != len(wantPaths) {
		t.Fatalf("bad candidates: %+v / %+v", doc.Candidates, doc.Members)
	}
	for index, member := range doc.Members {
		if member.Path != wantPaths[index] || member.Origin != wantOrigins[index] ||
			member.Digest != wantDigests[index] || member.Size != int64(len("candidate-one")) || !member.Exists {
			t.Fatalf("bad member %d: %+v", index, member)
		}
	}

	again, err := List(root, excluded)
	if err != nil || len(again) != 2 || again[1].GroupID != doc.GroupID {
		t.Fatalf("group ids are not stable: %+v, %v", again, err)
	}
}

func TestListHandlesMissingOriginal(t *testing.T) {
	root := t.TempDir()
	candidate := "missing.txt (conflitto da pc2 abcdef12).txt"
	writeTestFile(t, root, candidate, "copy")
	groups, err := List(root, nil)
	if err != nil || len(groups) != 1 {
		t.Fatalf("List = %+v, %v", groups, err)
	}
	if groups[0].OriginalInfo.Exists || groups[0].OriginalInfo.Digest != "" {
		t.Fatalf("missing original has metadata: %+v", groups[0].OriginalInfo)
	}
	if len(groups[0].Members) != 1 || !groups[0].Members[0].Exists {
		t.Fatalf("candidate metadata missing: %+v", groups[0].Members)
	}
}

func TestScanAcceptsSymlinkedRoot(t *testing.T) {
	realRoot := t.TempDir()
	writeTestFile(t, realRoot, "doc (conflitto da pc2 abcdef12).txt", "copy")
	link := filepath.Join(t.TempDir(), "NASBox")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	groups, err := Scan(link, nil)
	if err != nil || len(groups) != 1 || groups[0].Original != "doc.txt" {
		t.Fatalf("Scan through root symlink = %+v, %v", groups, err)
	}
}

func TestScanSkipsInternalDirectoriesAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	real := "doc (conflitto da pc2 abcdef12).txt"
	writeTestFile(t, root, real, "real")
	for _, internal := range []string{".sync-partial", ".sync-trash", ".nasbox-root"} {
		writeTestFile(t, root, internal+"/doc (conflitto da hidden abcdef12).txt", "hidden")
	}
	if err := os.Mkdir(filepath.Join(root, "dir (conflitto da pc2 abcdef12).txt"), 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "pipe (conflitto da pc2 abcdef12).txt")
	if err := makeFIFO(fifo); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	writeTestFile(t, outside, ".", "outside")
	if err := os.Symlink(outside, filepath.Join(root, "link (conflitto da pc2 abcdef12).txt")); err != nil {
		t.Fatal(err)
	}

	groups, err := Scan(root, nil)
	if err != nil || len(groups) != 1 || !reflect.DeepEqual(groups[0].Candidates, []string{real}) {
		t.Fatalf("Scan included special/internal files: %+v, %v", groups, err)
	}
}
