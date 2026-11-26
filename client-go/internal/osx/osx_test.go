package osx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWritableAndFreeBytes(t *testing.T) {
	dir := t.TempDir()
	if !Writable(dir) {
		t.Fatal("la cartella temporanea deve essere scrivibile")
	}
	if Writable(filepath.Join(dir, "non", "esiste")) {
		t.Fatal("una cartella inesistente non è scrivibile")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("il file di prova va rimosso: %v", entries)
	}
	free, total, ok := FreeBytes(dir)
	if !ok || total == 0 || free > total {
		t.Fatalf("spazio non plausibile: free=%d total=%d ok=%v", free, total, ok)
	}
	if OpenCommand(dir).Path == "" {
		t.Fatal("comando di apertura vuoto")
	}
}
