package ui

import (
	"fmt"
	"sync"
	"time"

	"fyne.io/fyne/v2"

	"nasbox-go/internal/engine"
)

// Desktop notifications are grouped: a burst of quick syncs (the realtime
// watcher can finish one every few seconds) must not stack a dozen almost
// identical popups on the user's desktop. We wait for a short quiet period and
// send one digest with the totals; a long burst still gets a digest every
// syncNotifyMaxWindow so feedback never disappears entirely.
const (
	syncNotifyQuiet     = 45 * time.Second
	syncNotifyMaxWindow = 10 * time.Minute
)

type syncNotifier struct {
	mu      sync.Mutex
	pending engine.SyncCounts
	passes  int
	since   time.Time
	timer   *time.Timer
	now     func() time.Time
	send    func(engine.SyncCounts, int)
}

func newSyncNotifier(send func(engine.SyncCounts, int)) *syncNotifier {
	return &syncNotifier{now: time.Now, send: send}
}

// add accumulates one successful sync. The notification itself is delivered
// by a timer, outside the event loop, so rapid completions collapse into one.
func (n *syncNotifier) add(counts engine.SyncCounts) {
	if counts.Uploads+counts.Downloads+counts.Deletes+counts.Conflicts == 0 {
		return
	}
	n.mu.Lock()
	now := n.now()
	if n.passes == 0 {
		n.since = now
	}
	n.passes++
	n.pending.Uploads += counts.Uploads
	n.pending.Downloads += counts.Downloads
	n.pending.Deletes += counts.Deletes
	n.pending.Conflicts += counts.Conflicts
	expired := now.Sub(n.since) >= syncNotifyMaxWindow
	n.mu.Unlock()

	if expired {
		n.flush()
		return
	}

	n.mu.Lock()
	if n.timer == nil {
		n.timer = time.AfterFunc(syncNotifyQuiet, n.flush)
	} else {
		n.timer.Reset(syncNotifyQuiet)
	}
	n.mu.Unlock()
}

// flush sends the digest accumulated so far, if any. Safe to call at any time
// (including when no timer is pending), e.g. when the client shuts down.
func (n *syncNotifier) flush() {
	n.mu.Lock()
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	if n.passes == 0 {
		n.mu.Unlock()
		return
	}
	counts, passes := n.pending, n.passes
	n.pending, n.passes, n.since = engine.SyncCounts{}, 0, time.Time{}
	n.mu.Unlock()

	if n.send != nil {
		n.send(counts, passes)
	}
}

// syncDigestBody renders the grouped notification text.
func syncDigestBody(counts engine.SyncCounts, passes int) string {
	label := lt(fmt.Sprintf("%d sincronizzazioni", passes), fmt.Sprintf("%d syncs", passes))
	return tr("ui.notify.sync_digest", map[string]any{
		"passes":    label,
		"uploads":   counts.Uploads,
		"downloads": counts.Downloads,
		"deletes":   counts.Deletes,
		"conflicts": counts.Conflicts,
	})
}

// sendSyncNotification formats one grouped digest and hands it to the desktop.
func (c *clientApp) sendSyncNotification(counts engine.SyncCounts, passes int) {
	body := syncDigestBody(counts, passes)
	fyne.Do(func() {
		if app := fyne.CurrentApp(); app != nil {
			app.SendNotification(&fyne.Notification{
				Title:   tr("ui.notify.sync_digest_title", nil),
				Content: body,
			})
		}
	})
}
