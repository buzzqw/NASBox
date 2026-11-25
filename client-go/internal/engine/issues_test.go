package engine

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestIsolateBatchFindsTheBadFilesAndTransfersTheRest(t *testing.T) {
	Issues = &issueBook{issues: map[string]*Issue{}, now: time.Now}
	paths := make([]string, 40)
	for index := range paths {
		paths[index] = fmt.Sprintf("f%02d", index)
	}
	var done []string
	err := isolateBatch(paths, PhaseUpload, 100, func(batch []string) error {
		for _, path := range batch {
			if path == "f07" || path == "f31" {
				return errors.New("permission denied: " + path)
			}
		}
		done = append(done, batch...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 38 {
		t.Fatalf("38 good files must be transferred, got %d", len(done))
	}
	issues := Issues.Snapshot()
	if len(issues) != 2 || issues[0].Path != "f07" && issues[1].Path != "f07" {
		t.Fatalf("issues: %+v", issues)
	}
}

func TestIsolateBatchAbortsOnGlobalFailures(t *testing.T) {
	Issues = &issueBook{issues: map[string]*Issue{}, now: time.Now}
	calls := 0
	err := isolateBatch([]string{"a", "b", "c"}, PhaseDownload, 50, func([]string) error {
		calls++
		return errors.New("ssh: exit status 255: Connection refused")
	})
	if err == nil || calls != 1 || Issues.Count() != 0 {
		t.Fatalf("global failure must abort at once: err=%v calls=%d issues=%d", err, calls, Issues.Count())
	}
}

func TestIssueBackoffAndHeld(t *testing.T) {
	clock := time.Unix(1000, 0)
	book := &issueBook{issues: map[string]*Issue{}, now: func() time.Time { return clock }}
	book.BeginPass()
	book.Report("docs/a.txt", PhaseUpload, "boom")
	if !book.Waiting("docs/a.txt") {
		t.Fatal("a failed file waits for its retry delay")
	}
	clock = clock.Add(61 * time.Second)
	if book.Waiting("docs/a.txt") {
		t.Fatal("after one minute it is tried again")
	}
	book.Report("docs/a.txt", PhaseUpload, "boom")
	if got := book.Snapshot()[0].NextTry.Sub(clock); got != 2*time.Minute {
		t.Fatalf("second failure waits 2 minutes, got %v", got)
	}
	book.Report("locked", PhaseRead, "in uso")
	if !book.Held("locked") || !book.Held("locked/inner.txt") || book.Held("lockedx") {
		t.Fatal("held covers the path and what is below it, nothing else")
	}
	book.BeginPass()
	book.EndPass() // the read problem was not seen again: the file is readable
	if book.Held("locked") || book.Count() != 1 {
		t.Fatalf("read problems disappear by themselves: %+v", book.Snapshot())
	}
}

func TestPendingDeletesFoldersAreGroupedAndSorted(t *testing.T) {
	book := &pendingBook{}
	var paths []string
	for index := 0; index < 5; index++ {
		paths = append(paths, fmt.Sprintf("SADE/SADE/images/%d.png", index))
	}
	paths = append(paths, "Docs/a.txt", "Docs/b.txt", "single.txt")
	book.Set(paths)
	folders := book.Folders(2)
	if len(folders) != 2 || folders[0].Folder != "SADE/SADE" || folders[0].Count != 5 || folders[1].Folder != "Docs/a.txt" && folders[1].Folder != "Docs" {
		t.Fatalf("folders: %+v", folders)
	}
}
