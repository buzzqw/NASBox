package serverd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// Lease holder heartbeat. The owner file (device|host|since|phase|done|total)
// is refreshed by the client with ACTIVITY_V1 frames; this sidecar records the
// PID of the --transfer-wait process that holds the flock plus the timestamp
// of the last frame, so the daemon can tell an abandoned lease from a slow one.
//
// A lease is only considered stale after the client has sent at least one
// heartbeat: older clients never send one and keep the previous behaviour
// (never force-released).
type leaseHolder struct {
	Device       string `json:"device"`
	Host         string `json:"host"`
	PID          int    `json:"pid"`
	Since        int64  `json:"since"`
	LastActivity int64  `json:"last_activity"`
}

func (c *Context) leaseHolderPath() string {
	return filepath.Join(c.StateDir, "lease-holder.json")
}

func (c *Context) leaseMaxAge() time.Duration {
	minutes, err := strconv.ParseInt(firstField(c.Config["SYNC_LOCK_MAX_AGE_MINUTES"]), 10, 64)
	if err != nil || minutes <= 0 {
		return 0
	}
	return time.Duration(minutes) * time.Minute
}

func (c *Context) writeLeaseHolder(holder leaseHolder) error {
	path := c.leaseHolderPath()
	data, err := json.Marshal(holder)
	if err != nil {
		return err
	}
	tmp := path + "." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Context) readLeaseHolder() (leaseHolder, bool) {
	data, err := os.ReadFile(c.leaseHolderPath())
	if err != nil {
		return leaseHolder{}, false
	}
	var holder leaseHolder
	if err := json.Unmarshal(data, &holder); err != nil {
		return leaseHolder{}, false
	}
	return holder, true
}

func (c *Context) touchLeaseHolder() {
	holder, ok := c.readLeaseHolder()
	if !ok {
		return
	}
	holder.LastActivity = time.Now().Unix()
	_ = c.writeLeaseHolder(holder)
}

// leaseState reports the idle time of the current lease and whether it has
// passed SYNC_LOCK_MAX_AGE_MINUTES after at least one client heartbeat.
func (c *Context) leaseState(now time.Time) (holder leaseHolder, held, stale bool, idle time.Duration) {
	maxAge := c.leaseMaxAge()
	holder, ok := c.readLeaseHolder()
	if !ok {
		return leaseHolder{}, false, false, 0
	}
	// The holder is written by the process that owns the flock and removed when
	// it exits, so its PID is the cheapest liveness probe. Avoid touching the
	// lock itself: a periodic flock probe could steal a grant window from a
	// client that is about to acquire it.
	held = processAlive(holder.PID)
	base := holder.LastActivity
	if base == 0 {
		base = holder.Since
	}
	if base > 0 {
		idle = now.Sub(time.Unix(base, 0))
		if idle < 0 {
			idle = 0
		}
	}
	stale = held && maxAge > 0 && holder.LastActivity > 0 && idle > maxAge
	return holder, held, stale, idle
}

// enforceLeaseMaxAge is the safety valve for a client that vanished while its
// SSH channel stayed half-open. It only kills a process that still looks like
// this server's --transfer-wait, never an unrelated PID that reused the number.
func (c *Context) enforceLeaseMaxAge() {
	holder, held, stale, idle := c.leaseState(time.Now())
	if !held || !stale {
		return
	}
	maxAge := c.leaseMaxAge()
	if !isTransferWaitProcess(holder.PID) {
		c.logEvent("lease_stale_unverified", map[string]any{
			"device": holder.Device, "host": holder.Host, "pid": holder.PID,
			"idle_seconds": int64(idle.Seconds()),
		})
		return
	}
	if err := syscall.Kill(holder.PID, syscall.SIGTERM); err != nil {
		c.logEvent("lease_stale_kill_failed", map[string]any{
			"device": holder.Device, "pid": holder.PID, "error": err.Error(),
		})
		return
	}
	c.logEvent("lease_stale_released", map[string]any{
		"device": holder.Device, "host": holder.Host, "pid": holder.PID,
		"idle_seconds": int64(idle.Seconds()), "max_age_minutes": int64(maxAge.Minutes()),
	})
	_ = os.Remove(c.leaseHolderPath())
	_ = os.Remove(c.paths().Owner)
}

// isTransferWaitProcess guards the force release against PID reuse.
func isTransferWaitProcess(pid int) bool {
	if pid <= 0 {
		return false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	command := string(data)
	return containsArg(command, "--transfer-wait") && containsArg(command, "nasbox-server")
}

func containsArg(command, needle string) bool {
	for _, part := range splitNUL(command) {
		if part == needle || (needle != "" && filepath.Base(part) == needle) {
			return true
		}
	}
	return false
}

func splitNUL(command string) []string {
	parts := make([]string, 0, 8)
	start := 0
	for index := 0; index < len(command); index++ {
		if command[index] == 0 {
			parts = append(parts, command[start:index])
			start = index + 1
		}
	}
	if start < len(command) {
		parts = append(parts, command[start:])
	}
	return parts
}
