package serverd

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Notifiche opzionali (NOTIFY_URL). Il formato è quello di ntfy: POST del testo
// con l'header Title; funziona anche con qualsiasi webhook che accetti un corpo
// di testo. I messaggi contengono solo conteggi e percentuali, mai nomi di file,
// e ogni tipo parte al massimo una volta ogni NOTIFY_MIN_HOURS (default 24).
//
//	NOTIFY_URL=https://ntfy.sh/il-mio-argomento-segreto
//	NOTIFY_DISK_PERCENT=90     avvisa quando il volume supera questa occupazione
//	NOTIFY_MIN_HOURS=24

var notifyHTTP = &http.Client{Timeout: 10 * time.Second}

func (c *Context) notifyURL() string {
	raw := strings.TrimSpace(c.Config["NOTIFY_URL"])
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return raw
}

// notifyOnce sends a message unless one of the same kind went out recently.
func (c *Context) notifyOnce(kind, title, body string) {
	target := c.notifyURL()
	if target == "" {
		return
	}
	stamp := filepath.Join(c.StateDir, "notify-"+kind+".stamp")
	every := time.Duration(parseConfigInt(c.Config["NOTIFY_MIN_HOURS"], 24)) * time.Hour
	if !stampOlderThan(stamp, every) {
		return
	}
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Title", title)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	response, err := notifyHTTP.Do(req)
	if err != nil {
		c.logEvent("notify_error", map[string]any{"kind": kind, "error": err.Error()})
		return
	}
	_ = response.Body.Close()
	if response.StatusCode >= 300 {
		c.logEvent("notify_error", map[string]any{"kind": kind, "status": response.StatusCode})
		return
	}
	touchStamp(stamp)
	c.logEvent("notify_sent", map[string]any{"kind": kind})
}

// diskUsedPercent returns the used percentage of the volume holding the share.
func (c *Context) diskUsedPercent() (int, bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(c.ShareRoot, &stat); err != nil || stat.Blocks == 0 {
		return 0, false
	}
	used := float64(stat.Blocks-stat.Bavail) / float64(stat.Blocks)
	return int(used*100 + 0.5), true
}

// maintainNotifications raises the alerts that need no other maintenance step.
func (c *Context) maintainNotifications() {
	if c.notifyURL() == "" {
		return
	}
	threshold := int(parseConfigInt(c.Config["NOTIFY_DISK_PERCENT"], 90))
	if percent, ok := c.diskUsedPercent(); ok && threshold > 0 && percent >= threshold {
		c.notifyOnce("disk", "NASBox: disco quasi pieno",
			fmt.Sprintf("Il volume è occupato al %d%% (soglia %d%%).", percent, threshold))
	}
}
