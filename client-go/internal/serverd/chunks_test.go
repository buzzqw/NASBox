package serverd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilityValuesDeclareChunkTransfer(t *testing.T) {
	ctx, _ := newTestContext(t)
	values := map[string]string{}
	for _, line := range capabilityValues(ctx) {
		key, value, found := strings.Cut(line, "=")
		if !found {
			t.Fatalf("capability senza valore: %q", line)
		}
		values[key] = value
	}
	if values["CHUNK_TRANSFER_AVAILABLE"] != "true" {
		t.Fatalf("CHUNK_TRANSFER_AVAILABLE = %q, want true", values["CHUNK_TRANSFER_AVAILABLE"])
	}
	found := false
	for _, capability := range strings.Split(values["CAPABILITIES"], ",") {
		if capability == "chunk_transfer_v3" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("chunk_transfer_v3 assente da CAPABILITIES: %q", values["CAPABILITIES"])
	}
}

func TestChunkStoreDeduplicatesAndAssembles(t *testing.T) {
	root := t.TempDir()
	store, err := NewChunkStore(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(strings.Repeat("NASBox chunk  ", 200000))
	d1, err := store.Put(payload)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := store.Put(bytes.Clone(payload))
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatal("identical bytes produced different digests")
	}
	objects, _, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if objects != 1 {
		t.Fatalf("want 1 stored object, got %d", objects)
	}
	file, size, err := store.Open(d1)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, size)
	if _, err := file.Read(data); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if !bytes.Equal(data, payload) {
		t.Fatal("stored chunk changed")
	}

	target := filepath.Join(t.TempDir(), "assembled")
	if err := store.Assemble([]string{d1, d2, d1}, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]byte{}, payload...), payload...), payload...)
	if !bytes.Equal(got, want) {
		t.Fatalf("assembled %d bytes, want %d", len(got), len(want))
	}
}

func TestSnapshotCreateRestore(t *testing.T) {
	root := t.TempDir()
	share := filepath.Join(root, "share")
	if err := os.MkdirAll(filepath.Join(share, "cartella"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte(strings.Repeat("abc123\n", 500000))
	if err := os.WriteFile(filepath.Join(share, "cartella", "file.txt"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewChunkStore(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := store.CreateSnapshot(share, []string{"cartella/file.txt"}, "test-device")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Files) != 1 || len(snap.Files[0].Chunks) < 2 {
		t.Fatalf("bad snapshot: %+v", snap.Files)
	}
	loaded, err := store.LoadSnapshot(snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != snap.ID {
		t.Fatal("snapshot id changed")
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := store.RestoreFile(loaded, "cartella/file.txt", target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("restored %d bytes, want %d", len(got), len(content))
	}
	objects, _, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if objects != len(snap.Files[0].Chunks) {
		t.Fatalf("objects=%d chunks=%d", objects, len(snap.Files[0].Chunks))
	}
}
