package remote

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nasbox-go/internal/config"
)

func withLeaseCommand(t *testing.T, script string) {
	t.Helper()
	old := leaseCommand
	leaseCommand = func(*Client) *exec.Cmd { return exec.Command("sh", "-c", script) }
	t.Cleanup(func() { leaseCommand = old })
}

// The fake server behaves like --transfer-wait: it answers NASBOX_LOCKED and
// holds the lease until stdin reaches EOF.
func TestLeaseIsReleasedByClosingStdin(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "released")
	request := filepath.Join(t.TempDir(), "request")
	withLeaseCommand(t, `dd bs=1 count=60 of=`+request+` 2>/dev/null; echo NASBOX_LOCKED; cat >/dev/null; echo ok > `+marker)
	client := New(config.New(""))
	release, err := acquireTransferLease(client, "dev-1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("lease ended before release")
	}
	started := time.Now()
	release()
	release() // idempotent
	if time.Since(started) > 5*time.Second {
		t.Fatal("release must not wait for a timeout")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the server side did not see EOF: lease would stay held")
	}
	content, _ := os.ReadFile(request)
	if !strings.HasPrefix(string(content), "TRANSFER_WAIT_V1\x00dev-1\x000\x00") {
		t.Fatalf("bad request payload: %q", content)
	}
}

func TestLeaseSendsActivityHeartbeats(t *testing.T) {
	frames := filepath.Join(t.TempDir(), "frames")
	withLeaseCommand(t, `dd bs=1 count=60 of=/dev/null 2>/dev/null; echo NASBOX_LOCKED; cat > `+frames)
	old := leaseHeartbeatInterval
	leaseHeartbeatInterval = 80 * time.Millisecond
	t.Cleanup(func() { leaseHeartbeatInterval = old })

	client := New(config.New(""))
	release, err := acquireTransferLease(client, "dev-1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	release()

	data, err := os.ReadFile(frames)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), "ACTIVITY_V1\x00lease\x000\x000\x00"); count < 3 {
		t.Fatalf("attesi almeno 3 heartbeat durante il lease, trovati %d", count)
	}
}

func TestLeaseBusyTimesOutAndStopsTheSession(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "still-running")
	withLeaseCommand(t, `cat >/dev/null; sleep 30; echo late > `+marker)
	client := New(config.New(""))
	started := time.Now()
	_, err := acquireTransferLease(client, "dev-1", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "occupato") {
		t.Fatalf("busy lease must report contention, got %v", err)
	}
	if time.Since(started) > 10*time.Second {
		t.Fatal("busy lease must give up quickly")
	}
}

func TestLeaseRefusedReportsServerError(t *testing.T) {
	withLeaseCommand(t, `cat >/dev/null & echo "lock non disponibile" >&2; exit 1`)
	client := New(config.New(""))
	if _, err := acquireTransferLease(client, "dev-1", 5*time.Second); err == nil {
		t.Fatal("a failed session must not be treated as a lease")
	}
}
