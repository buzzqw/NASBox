package serverd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotifyOnceSendsTextWithTitleAndRateLimits(t *testing.T) {
	var hits atomic.Int32
	var gotTitle, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotTitle = r.Header.Get("Title")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
	}))
	defer server.Close()
	ctx, _ := newWebTestContext(t, "")

	ctx.notifyOnce("disk", "titolo", "corpo") // senza NOTIFY_URL: niente
	if hits.Load() != 0 {
		t.Fatal("senza NOTIFY_URL non si invia nulla")
	}
	ctx.Config["NOTIFY_URL"] = server.URL
	ctx.notifyOnce("disk", "NASBox: disco quasi pieno", "occupato al 95%")
	ctx.notifyOnce("disk", "NASBox: disco quasi pieno", "occupato al 95%")
	if hits.Load() != 1 || gotTitle != "NASBox: disco quasi pieno" || gotBody != "occupato al 95%" {
		t.Fatalf("hits=%d title=%q body=%q", hits.Load(), gotTitle, gotBody)
	}
	ctx.notifyOnce("fsck", "altro tipo", "x") // un altro tipo parte comunque
	if hits.Load() != 2 {
		t.Fatalf("ogni tipo ha il suo limite: %d", hits.Load())
	}
}

func TestNotifyRejectsNonHTTPAndFailuresDoNotConsumeTheSlot(t *testing.T) {
	ctx, _ := newWebTestContext(t, "")
	for _, bad := range []string{"", "ftp://x/y", "file:///etc/passwd", "javascript:alert(1)", "http://"} {
		ctx.Config["NOTIFY_URL"] = bad
		if ctx.notifyURL() != "" {
			t.Fatalf("URL %q non deve essere accettato", bad)
		}
	}
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "no", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	ctx.Config["NOTIFY_URL"] = server.URL
	ctx.notifyOnce("disk", "t", "b")
	fail.Store(false)
	ctx.notifyOnce("disk", "t", "b")
	events, _ := ctxEvents(ctx)
	if !strings.Contains(events, "notify_error") || !strings.Contains(events, "notify_sent") {
		t.Fatalf("un invio fallito non deve bloccare il successivo: %s", events)
	}
}

func TestDiskPercentThreshold(t *testing.T) {
	ctx, _ := newWebTestContext(t, "")
	percent, ok := ctx.diskUsedPercent()
	if !ok || percent < 0 || percent > 100 {
		t.Fatalf("occupazione non plausibile: %d %v", percent, ok)
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer server.Close()
	ctx.Config["NOTIFY_URL"] = server.URL
	ctx.Config["NOTIFY_DISK_PERCENT"] = "100"
	ctx.maintainNotifications()
	ctx.Config["NOTIFY_DISK_PERCENT"] = "1"
	ctx.maintainNotifications()
	time.Sleep(10 * time.Millisecond)
	if percent >= 1 && hits.Load() != 1 {
		t.Fatalf("con soglia 1%% deve avvisare una volta: %d", hits.Load())
	}
}

func ctxEvents(ctx *Context) (string, error) {
	data, err := readFileString(ctx.paths().Events)
	return data, err
}

func readFileString(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}
