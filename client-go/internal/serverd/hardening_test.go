package serverd

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMaintainStateFilesRemovesStaleDeltaDirs(t *testing.T) {
	ctx, _ := newTestContext(t)
	p := ctx.paths()
	live := filepath.Join(p.Dir, ".manifest-delta."+strconv.Itoa(os.Getpid())+".live")
	dead := filepath.Join(p.Dir, ".manifest-delta.999999.dead")
	fresh := filepath.Join(p.Dir, ".manifest-delta.999998.fresh")
	for _, dir := range []string{live, dead, fresh} {
		if err := os.MkdirAll(filepath.Join(dir, "base"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ageFile(t, dead, 72*time.Hour)
	ageFile(t, live, 72*time.Hour)

	ctx.maintainStateFiles()

	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("la directory delta di un processo morto doveva essere rimossa: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("la directory delta di un processo vivo non va rimossa: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("la directory delta recente non va rimossa: %v", err)
	}
}

func TestRestoreFileRejectsCorruptedChunk(t *testing.T) {
	ctx, _ := newTestContext(t)
	store, err := ctx.chunkStore()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("contenuto")
	digest, err := store.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	whole := sha256.Sum256(data)
	snapshot := &Snapshot{
		ID: "snap",
		Files: []SnapshotFile{{
			Path: encodeField("a.txt"), Digest: hex.EncodeToString(whole[:]),
			Size: int64(len(data)), Chunks: []string{digest},
		}},
	}
	// Same length, different bytes: only the digest can tell them apart.
	if err := os.WriteFile(store.objectPath(digest), []byte("CONTENUTO"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored.txt")
	if err := store.RestoreFile(snapshot, "a.txt", target); err == nil {
		t.Fatal("un restore con un chunk corrotto non deve avere successo")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("il file corrotto non deve restare su disco: %v", err)
	}
}

func TestAccessLogRotatesWithinKeepLimit(t *testing.T) {
	ctx, _ := newWebTestContext(t, "")
	ctx.Config["ACCESS_LOG_MAX_BYTES"] = "200"
	ctx.Config["ACCESS_LOG_KEEP"] = "2"
	logger := ctx.accessLog()
	for index := 0; index < 20; index++ {
		logger.write(strings.Repeat("x", 60) + "\n")
	}
	logger.Close()

	path := filepath.Join(ctx.StateDir, "access.log")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 200 {
		t.Fatalf("access.log supera il limite dopo la rotazione: %d", info.Size())
	}
	for _, suffix := range []string{".1", ".2"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Fatalf("generazione %s mancante: %v", suffix, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatal("non devono restare più generazioni di ACCESS_LOG_KEEP")
	}
}

func TestWebAccessLogRedactsShareTokens(t *testing.T) {
	ctx, _ := newWebTestContext(t, "")
	token := strings.Repeat("a", 32)
	request := httptest.NewRequest("GET", "/s/"+token, nil)
	ctx.webHandler().ServeHTTP(httptest.NewRecorder(), request)

	ctx.accessLog().Close()
	data, err := os.ReadFile(filepath.Join(ctx.StateDir, "access.log"))
	if err != nil {
		t.Fatal(err)
	}
	line := string(data)
	if !strings.Contains(line, "/s/[token]") {
		t.Fatalf("il percorso del link non è stato normalizzato: %q", line)
	}
	if strings.Contains(line, token) {
		t.Fatalf("il token del link non deve finire nel log: %q", line)
	}
}

func TestLeaseStaleNeedsHeartbeatAndHeldLease(t *testing.T) {
	ctx, _ := newWebTestContext(t, "")
	ctx.Config["SYNC_LOCK_MAX_AGE_MINUTES"] = "10"

	now := time.Now()
	// A legacy client never sends a heartbeat: age must never force-release it.
	if err := ctx.writeLeaseHolder(leaseHolder{Device: "dev", PID: os.Getpid(), Since: now.Add(-time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, _, stale, _ := ctx.leaseState(now); stale {
		t.Fatal("un lease senza heartbeat non deve risultare scaduto")
	}

	if err := ctx.writeLeaseHolder(leaseHolder{
		Device: "dev", PID: os.Getpid(),
		Since: now.Add(-time.Hour).Unix(), LastActivity: now.Add(-time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, stale, _ := ctx.leaseState(now); !stale {
		t.Fatal("un lease con heartbeat vecchio deve risultare scaduto")
	}

	if err := ctx.writeLeaseHolder(leaseHolder{
		Device: "dev", PID: os.Getpid(),
		Since: now.Add(-time.Hour).Unix(), LastActivity: now.Add(-time.Minute).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, stale, _ := ctx.leaseState(now); stale {
		t.Fatal("un lease con heartbeat recente non deve risultare scaduto")
	}
}

func TestSelfCheckFailsWithStoppedDaemon(t *testing.T) {
	ctx, _ := newWebTestContext(t, "")
	if err := cmdSelfCheck(ctx); err == nil {
		t.Fatal("--self-check deve fallire quando il demone non è in esecuzione")
	}
}
