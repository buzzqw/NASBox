package conflicts

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStormTrackerReusesVersionsAndLimitsCopies(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "doc.txt", "original")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tracker := NewStormTracker(StormConfig{
		ConflictStormLimit:         2,
		ConflictStormWindowSeconds: 3600,
	})
	now := time.Unix(1_700_000_000, 0)
	tracker.now = func() time.Time { return now }

	first, err := tracker.Create(root, "doc.txt", "pc2", "digest-1")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "doc.txt", "second")
	second, err := tracker.Create(root, "doc.txt", "pc2", "digest-2")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("versions below the limit shared a copy: %q", first)
	}

	writeTestFile(t, root, "doc.txt", "third")
	third, err := tracker.Create(root, "doc.txt", "pc2", "digest-3")
	if err != nil {
		t.Fatal(err)
	}
	if third != first {
		t.Fatalf("storm did not reuse the oldest slot: first=%q third=%q", first, third)
	}
	requireTestContent(t, root, first, "third")

	writeTestFile(t, root, "doc.txt", "retry")
	retry, err := tracker.Create(root, "doc.txt", "pc2", "digest-1")
	if err != nil || retry != first {
		t.Fatalf("same origin/digest was not deterministic: %q, %v; want %q", retry, err, first)
	}
	requireTestContent(t, root, first, "retry")

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 { // original plus exactly two conflict copies
		t.Fatalf("storm limit leaked copies: %v", entries)
	}

	plan, err := tracker.Plan(root, "doc.txt", "pc3", "digest-1")
	if err != nil || plan.Storm {
		t.Fatalf("a different origin shared another device's limit: %+v, %v", plan, err)
	}
}

func TestStormWindowExpiryStartsAFreshBudget(t *testing.T) {
	tracker := NewStormTracker(StormConfig{
		ConflictStormLimit:         1,
		ConflictStormWindowSeconds: 60,
	})
	now := time.Unix(1_700_000_000, 0)
	tracker.now = func() time.Time { return now }
	root := t.TempDir()

	first, err := tracker.Plan(root, "doc.txt", "pc2", "one")
	if err != nil || first.Storm {
		t.Fatalf("first plan = %+v, %v", first, err)
	}
	now = now.Add(30 * time.Second)
	storm, err := tracker.Plan(root, "doc.txt", "pc2", "two")
	if err != nil || !storm.Storm || storm.Path != first.Path || !storm.Reused {
		t.Fatalf("storm plan = %+v, %v; want reuse of %q", storm, err, first.Path)
	}
	retry, err := tracker.Plan(root, "doc.txt", "pc2", "two")
	if err != nil || !retry.Reused || retry.Storm || retry.Path != storm.Path {
		t.Fatalf("retry plan = %+v, %v", retry, err)
	}

	now = now.Add(61 * time.Second) // both records are now outside the window
	fresh, err := tracker.Plan(root, "doc.txt", "pc2", "three")
	if err != nil || fresh.Storm || fresh.Reused {
		t.Fatalf("expired window still limited the new version: %+v, %v", fresh, err)
	}
	if fresh.Path == first.Path {
		t.Fatalf("expired slot was reused for a new digest: %q", fresh.Path)
	}
}

func TestStormTrackerSameDigestDifferentOriginAndPath(t *testing.T) {
	tracker := NewStormTracker(StormConfig{})
	root := t.TempDir()
	first, err := tracker.Plan(root, "a.txt", "pc2", "same")
	if err != nil {
		t.Fatal(err)
	}
	otherOrigin, err := tracker.Plan(root, "a.txt", "pc3", "same")
	if err != nil || otherOrigin.Path == first.Path {
		t.Fatalf("origins shared a name: %+v vs %+v (%v)", first, otherOrigin, err)
	}
	otherPath, err := tracker.Plan(root, "b.txt", "pc2", "same")
	if err != nil || otherPath.Path == first.Path {
		t.Fatalf("originals shared a name: %+v vs %+v (%v)", first, otherPath, err)
	}
}

func TestStormTrackerConcurrentRetries(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "doc.txt", "payload")
	tracker := NewStormTracker(StormConfig{ConflictStormLimit: 2})

	const workers = 16
	paths := make([]string, workers)
	errors := make([]error, workers)
	var wait sync.WaitGroup
	for index := range paths {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			paths[index], errors[index] = tracker.Create(root, "doc.txt", "pc2", "same-digest")
		}(index)
	}
	wait.Wait()
	for index := range paths {
		if errors[index] != nil || paths[index] != paths[0] {
			t.Fatalf("worker %d = %q, %v; want %q", index, paths[index], errors[index], paths[0])
		}
	}
	requireTestContent(t, root, paths[0], "payload")
}

func TestDefaultStormConfigurationMatchesPython(t *testing.T) {
	tracker := NewStormTracker(StormConfig{})
	if tracker.limit != 3 || tracker.window != time.Hour {
		t.Fatalf("default storm config = limit %d, window %s", tracker.limit, tracker.window)
	}
	for _, config := range []StormConfig{
		{ConflictStormLimit: -1, ConflictStormWindowSeconds: -1},
		{ConflictStormLimit: 1, ConflictStormWindowSeconds: 1},
	} {
		tracker := NewStormTracker(config)
		if tracker.limit <= 0 || tracker.window <= 0 {
			t.Fatalf("invalid normalized config: %+v", config)
		}
	}
}

func TestCreateRejectsSpecialDestination(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "doc.txt", "payload")
	candidate, err := ConflictName("doc.txt", "pc2", "digest")
	if err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	writeTestFile(t, outside, ".", "outside")
	if err := os.Symlink(outside, filepath.Join(root, candidate)); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(root, "doc.txt", "pc2", "digest"); err == nil {
		t.Fatal("Create accepted a symlink destination")
	}
	requireTestContent(t, outsideDir, "outside.txt", "outside")
}

func TestCreateRejectsSpecialSource(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe")
	if err := makeFIFO(fifo); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	if _, err := Create(root, "pipe", "pc2", "digest"); err == nil {
		t.Fatal("Create accepted a FIFO source")
	}
}
