package serverd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ageFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestChunkGCKeepsLiveSnapshotAndRecentChunks(t *testing.T) {
	store, err := NewChunkStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	put := func(text string) string {
		digest, err := store.Put([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	liveChunk, snapChunk, orphan, recentOrphan := put("vivo"), put("snapshot"), put("orfano"), put("recente")
	deadMetaChunk := put("meta-morto")
	for _, d := range []string{liveChunk, snapChunk, orphan, deadMetaChunk} {
		ageFile(t, store.objectPath(d), 72*time.Hour)
	}
	liveFile := "1111111111111111111111111111111111111111111111111111111111111111"
	deadFile := "2222222222222222222222222222222222222222222222222222222222222222"
	if err := store.SaveFileMeta(liveFile, []string{liveChunk}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFileMeta(deadFile, []string{deadMetaChunk}); err != nil {
		t.Fatal(err)
	}
	ageFile(t, store.fileMetaPath(liveFile), 72*time.Hour)
	ageFile(t, store.fileMetaPath(deadFile), 72*time.Hour)
	if err := store.SaveSnapshot(&Snapshot{ID: "s1", Files: []SnapshotFile{{Path: "a", Chunks: []string{snapChunk}}}}); err != nil {
		t.Fatal(err)
	}

	opts := GCOptions{Live: map[string]bool{liveFile: true}, Grace: DefaultChunkGrace}
	dry := opts
	dry.DryRun = true
	report, err := store.GC(dry)
	if err != nil {
		t.Fatal(err)
	}
	if report.ObjectsRemoved != 2 || report.MetasRemoved != 1 || !store.Has(orphan) {
		t.Fatalf("dry-run: %+v (orphan presente=%v)", report, store.Has(orphan))
	}

	report, err = store.GC(opts)
	if err != nil {
		t.Fatal(err)
	}
	if report.ObjectsRemoved != 2 || report.MetasRemoved != 1 {
		t.Fatalf("GC: %+v", report)
	}
	for name, want := range map[string]bool{
		liveChunk: true, snapChunk: true, recentOrphan: true, orphan: false, deadMetaChunk: false,
	} {
		if store.Has(name) != want {
			t.Fatalf("chunk %s presente=%v, atteso %v", name[:8], store.Has(name), want)
		}
	}
	if _, err := store.LoadFileMeta(liveFile); err != nil {
		t.Fatalf("meta vivo perso: %v", err)
	}
	if _, err := store.LoadFileMeta(deadFile); err == nil {
		t.Fatal("meta morto ancora presente")
	}
}

func TestChunkGCRefusesWhenSnapshotUnreadable(t *testing.T) {
	store, _ := NewChunkStore(t.TempDir())
	digest, _ := store.Put([]byte("x"))
	ageFile(t, store.objectPath(digest), 72*time.Hour)
	if err := os.WriteFile(filepath.Join(store.root, "snapshots", "rotto.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GC(GCOptions{Grace: DefaultChunkGrace}); err == nil {
		t.Fatal("con uno snapshot illeggibile la GC non deve cancellare nulla")
	}
	if !store.Has(digest) {
		t.Fatal("chunk cancellato nonostante lo snapshot illeggibile")
	}
}

func TestChunkTouchProtectsReusedChunk(t *testing.T) {
	store, _ := NewChunkStore(t.TempDir())
	digest, _ := store.Put([]byte("riusato"))
	ageFile(t, store.objectPath(digest), 72*time.Hour)
	store.Touch(digest)
	report, err := store.GC(GCOptions{Grace: DefaultChunkGrace})
	if err != nil {
		t.Fatal(err)
	}
	if report.ObjectsRemoved != 0 || !store.Has(digest) {
		t.Fatalf("chunk riusato raccolto dalla GC: %+v", report)
	}
}

func TestChunkFsckDetectsAndRepairsCorruption(t *testing.T) {
	store, _ := NewChunkStore(t.TempDir())
	good, _ := store.Put([]byte("buono"))
	bad, _ := store.Put([]byte("da rovinare"))
	if err := os.WriteFile(store.objectPath(bad), []byte("rovinato"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileDigest := "3333333333333333333333333333333333333333333333333333333333333333"
	if err := store.SaveFileMeta(fileDigest, []string{good, bad}); err != nil {
		t.Fatal(err)
	}
	report, err := store.Fsck(false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Corrupt != 1 || report.MetasBroken != 1 || report.Repaired != 0 || !fileExists(store.objectPath(bad)) {
		t.Fatalf("fsck senza repair: %+v", report)
	}
	report, err = store.Fsck(true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Repaired != 2 || fileExists(store.objectPath(bad)) || fileExists(store.fileMetaPath(fileDigest)) {
		t.Fatalf("fsck con repair: %+v", report)
	}
	if !store.Has(good) {
		t.Fatal("il chunk sano non deve essere toccato")
	}
	if again, _ := store.Fsck(false); again.Corrupt+again.MetasBroken != 0 {
		t.Fatalf("store non pulito dopo repair: %+v", again)
	}
}

func TestChunkFsckReportsSnapshotWithMissingChunk(t *testing.T) {
	store, _ := NewChunkStore(t.TempDir())
	missing := "4444444444444444444444444444444444444444444444444444444444444444"
	if err := store.SaveSnapshot(&Snapshot{ID: "s", Files: []SnapshotFile{{Path: "a", Chunks: []string{missing}}}}); err != nil {
		t.Fatal(err)
	}
	report, _ := store.Fsck(true)
	if report.SnapshotsBad != 1 || report.Repaired != 0 {
		t.Fatalf("gli snapshot si segnalano e non si riparano: %+v", report)
	}
}
