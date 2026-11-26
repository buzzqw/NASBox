package ui

import (
	"strings"
	"sync"
	"testing"
	"time"

	"nasbox-go/internal/engine"
	"nasbox-go/internal/i18n"
)

func TestSyncNotifierGroupsQuickCompletions(t *testing.T) {
	type delivered struct {
		counts engine.SyncCounts
		passes int
	}
	var (
		mu    sync.Mutex
		got   []delivered
		clock = time.Now()
	)
	notifier := newSyncNotifier(func(counts engine.SyncCounts, passes int) {
		mu.Lock()
		got = append(got, delivered{counts: counts, passes: passes})
		mu.Unlock()
	})
	notifier.now = func() time.Time { return clock }

	notifier.add(engine.SyncCounts{Uploads: 1})
	clock = clock.Add(6 * time.Second)
	notifier.add(engine.SyncCounts{Downloads: 2, Conflicts: 1})
	clock = clock.Add(6 * time.Second)
	notifier.add(engine.SyncCounts{Deletes: 3})

	mu.Lock()
	early := len(got)
	mu.Unlock()
	if early != 0 {
		t.Fatalf("notification sent before the quiet window: %d", early)
	}

	notifier.flush()
	notifier.flush() // an explicit flush must not send an empty digest

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("want one grouped notification, got %d", len(got))
	}
	if got[0].passes != 3 {
		t.Errorf("want 3 grouped passes, got %d", got[0].passes)
	}
	want := engine.SyncCounts{Uploads: 1, Downloads: 2, Deletes: 3, Conflicts: 1}
	if got[0].counts != want {
		t.Errorf("want totals %+v, got %+v", want, got[0].counts)
	}
}

func TestSyncNotifierCapsLongBursts(t *testing.T) {
	var (
		mu     sync.Mutex
		passes []int
		clock  = time.Now()
	)
	notifier := newSyncNotifier(func(_ engine.SyncCounts, count int) {
		mu.Lock()
		passes = append(passes, count)
		mu.Unlock()
	})
	notifier.now = func() time.Time { return clock }

	for i := 0; i < 25; i++ {
		notifier.add(engine.SyncCounts{Uploads: 1})
		clock = clock.Add(time.Minute)
	}

	notifier.flush() // deliver the tail of the burst too
	mu.Lock()
	defer mu.Unlock()
	if len(passes) < 2 {
		t.Fatalf("a burst longer than %s must produce more than one digest, got %v", syncNotifyMaxWindow, passes)
	}
	total := 0
	for _, count := range passes {
		total += count
	}
	if total != 25 {
		t.Errorf("every pass must appear in a digest, got %d of 25", total)
	}
}

func TestSyncDigestBodyHasTotalsAndNoUnresolvedPlaceholders(t *testing.T) {
	old := i18n.Language
	t.Cleanup(func() { i18n.Language = old })
	i18n.Language = "it"
	body := syncDigestBody(engine.SyncCounts{Uploads: 2, Downloads: 3, Deletes: 4, Conflicts: 5}, 7)
	for _, want := range []string{
		"7 sincronizzazioni", "2 caricamenti", "3 download", "4 eliminazioni", "5 conflitti",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("digest %q does not contain %q", body, want)
		}
	}
	if strings.Contains(body, "{") {
		t.Errorf("unresolved placeholder in %q", body)
	}
}

func TestSyncNotifierIgnoresTimerAfterFlush(t *testing.T) {
	called := 0
	notifier := newSyncNotifier(func(_ engine.SyncCounts, _ int) { called++ })
	notifier.flush()
	if called != 0 {
		t.Fatal("empty digest must not send a notification")
	}
	// A stale timer firing after the digest was delivered is harmless.
	notifier.flush()
	if called != 0 {
		t.Fatal("duplicate flush must not send a notification")
	}
}
