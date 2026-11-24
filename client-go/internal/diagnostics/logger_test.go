package diagnostics

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func TestOpenUsesPrivateXDGPaths(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	logger, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	wantDir := filepath.Join(base, "sync-daemon")
	if logger.Dir() != wantDir {
		t.Fatalf("Dir() = %q, want %q", logger.Dir(), wantDir)
	}
	for _, path := range []string{logger.logPath, logger.eventPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
	info, err := os.Stat(wantDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode = %o, want 700", info.Mode().Perm())
	}
}

func TestTextAndJSONLFormatsRoundTrip(t *testing.T) {
	logger, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	path := "cartella/con\nnewline.txt"
	detail := "dettaglio\tcon \"virgolette\" e password=non-esportata"
	if err := logger.Log("SYNC_ERROR", path, detail); err != nil {
		t.Fatal(err)
	}
	if err := logger.Event("UPLOAD", path, detail); err != nil {
		t.Fatal(err)
	}

	text, err := os.ReadFile(logger.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(text), "\n") != 1 {
		t.Fatalf("embedded newline split the text log: %q", text)
	}
	tail, err := logger.Tail(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 1 || tail[0].Action != "SYNC_ERROR" || tail[0].Level != "ERROR" ||
		tail[0].Path != path || tail[0].Detail != detail {
		t.Fatalf("text round trip failed: %+v", tail)
	}

	eventData, err := os.ReadFile(logger.eventPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(eventData), "\n") != 1 {
		t.Fatalf("event was not one JSON line: %q", eventData)
	}
	var diskEvent map[string]any
	if err := json.Unmarshal(bytesTrimLine(eventData), &diskEvent); err != nil {
		t.Fatal(err)
	}
	if len(diskEvent) != 4 {
		t.Fatalf("event schema drifted from Python compatibility: %v", diskEvent)
	}
	for _, key := range []string{"ts", "action", "path", "detail"} {
		if _, ok := diskEvent[key]; !ok {
			t.Fatalf("event missing %q: %v", key, diskEvent)
		}
	}
	recent, err := logger.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].Path != path || recent[0].Detail != detail {
		t.Fatalf("event round trip failed: %+v", recent)
	}
}

func bytesTrimLine(data []byte) []byte {
	return []byte(strings.TrimSpace(string(data)))
}

func TestRotationRetainsTwoBackupsAndReadsAcrossThem(t *testing.T) {
	logger, err := openLogger(t.TempDir(), 256, 2)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 12; index++ {
		action := fmt.Sprintf("ACTION_%02d", index)
		if err := logger.Log(action, "path", strings.Repeat("x", 40)); err != nil {
			t.Fatal(err)
		}
		if err := logger.Event(action, "path", strings.Repeat("y", 40)); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{logger.logPath, logger.eventPath} {
		for _, suffix := range []string{"", ".1", ".2"} {
			info, err := os.Stat(base + suffix)
			if err != nil {
				t.Fatalf("missing retained file %s: %v", base+suffix, err)
			}
			if info.Size() > 256 {
				t.Fatalf("%s exceeds rotation limit: %d", base+suffix, info.Size())
			}
		}
		if _, err := os.Stat(base + ".3"); !os.IsNotExist(err) {
			t.Fatalf("old third backup was retained: %v", err)
		}
	}
	tail, err := logger.Tail(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 6 || tail[0].Action != "ACTION_06" || tail[5].Action != "ACTION_11" {
		t.Fatalf("Tail did not read oldest-to-newest across rotations: %+v", tail)
	}
	recent, err := logger.Recent(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) < 6 || recent[len(recent)-1].Action != "ACTION_11" {
		t.Fatalf("Recent did not include the current rotated event file: %+v", recent)
	}
	limited, err := logger.Tail(2)
	if err != nil || len(limited) != 2 || limited[1].Action != "ACTION_11" {
		t.Fatalf("Tail limit failed: %+v, %v", limited, err)
	}
}

func TestConcurrentWritesAndReadsAreSerialised(t *testing.T) {
	logger, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const (
		workers   = 16
		perWorker = 40
	)
	var wait sync.WaitGroup
	failures := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for index := 0; index < perWorker; index++ {
				action := fmt.Sprintf("WORK_%02d_%02d", worker, index)
				if err := logger.Log(action, "file.txt", "detail"); err != nil {
					failures <- err
					return
				}
				if err := logger.Event(action, "file.txt", "detail"); err != nil {
					failures <- err
					return
				}
			}
		}(worker)
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	tail, err := logger.Tail(0)
	if err != nil {
		t.Fatal(err)
	}
	recent, err := logger.Recent(0)
	if err != nil {
		t.Fatal(err)
	}
	want := workers * perWorker
	if len(tail) != want || len(recent) != want {
		t.Fatalf("lost concurrent records: tail=%d recent=%d want=%d", len(tail), len(recent), want)
	}
	seen := make(map[string]bool, want)
	for _, entry := range tail {
		if seen[entry.Action] {
			t.Fatalf("duplicated action %q", entry.Action)
		}
		seen[entry.Action] = true
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
	if err := logger.Log("AFTER_CLOSE", "", ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after Close = %v, want ErrClosed", err)
	}
	if err := logger.Event("AFTER_CLOSE", "", ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("event after Close = %v, want ErrClosed", err)
	}
}

func TestMalformedRecordsAreSkipped(t *testing.T) {
	logger, err := OpenDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.Log("VALID", "a", "b"); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(logger.logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("not a diagnostic record\n")
	_ = file.Close()
	if err := logger.Event("VALID", "a", "b"); err != nil {
		t.Fatal(err)
	}
	file, err = os.OpenFile(logger.eventPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("{broken json\n")
	_ = file.Close()
	tail, err := logger.Tail(0)
	if err != nil || len(tail) != 1 || tail[0].Action != "VALID" {
		t.Fatalf("malformed text record was not skipped: %+v, %v", tail, err)
	}
	recent, err := logger.Recent(0)
	if err != nil || len(recent) != 1 || recent[0].Action != "VALID" {
		t.Fatalf("malformed event was not skipped: %+v, %v", recent, err)
	}
}

func TestProbeLocalLockReportsHeldAndFree(t *testing.T) {
	dir := t.TempDir()
	if status := ProbeLocalLock(dir); status.State != "free" {
		t.Fatalf("new lock should be free: %+v", status)
	}
	file, err := os.OpenFile(filepath.Join(dir, "client.instance.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if status := ProbeLocalLock(dir); status.State != "held" {
		t.Fatalf("held lock not detected: %+v", status)
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	if status := ProbeLocalLock(dir); status.State != "free" {
		t.Fatalf("released lock not detected: %+v", status)
	}
}
