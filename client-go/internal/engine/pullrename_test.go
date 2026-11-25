package engine

import (
	"os"
	"path/filepath"
	"testing"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/state"
)

// A rename made on another PC reaches this one as DELETE old + PUT new in the
// journal. Pairing them by digest and size turns the pull into a local move
// instead of a second download of bytes that are already here.
func TestPlanPullRenamesPairsIdenticalBytes(t *testing.T) {
	local := map[string]localFile{
		"Progetto/sub/f1.txt": {Digest: digest("uno"), Size: 3},
		"Progetto/sub/f2.txt": {Digest: digest("due"), Size: 3},
	}
	manifest := map[string]classify.RemoteState{
		"Progetto 2026/sub/f1.txt": {Kind: "FILE", Digest: digest("uno"), Size: 3},
		"Progetto 2026/sub/f2.txt": {Kind: "FILE", Digest: digest("due"), Size: 3},
	}
	moves, downloads, deletes := planPullRenames(
		[]string{"Progetto 2026/sub/f2.txt", "Progetto 2026/sub/f1.txt"},
		[]string{"Progetto/sub/f1.txt", "Progetto/sub/f2.txt"},
		local, manifest,
	)
	if len(moves) != 2 || len(downloads) != 0 || len(deletes) != 0 {
		t.Fatalf("rinomina non riconosciuta: moves=%v downloads=%v deletes=%v", moves, downloads, deletes)
	}
	if moves[0].Src != "Progetto/sub/f1.txt" || moves[0].Dst != "Progetto 2026/sub/f1.txt" {
		t.Fatalf("accoppiamento errato: %v", moves)
	}
	if moves[1].Src != "Progetto/sub/f2.txt" || moves[1].Dst != "Progetto 2026/sub/f2.txt" {
		t.Fatalf("accoppiamento errato: %v", moves)
	}
}

// Different bytes are not a rename: they must stay a real download and a real
// deletion, or the client would overwrite a changed file with the wrong one.
func TestPlanPullRenamesKeepsDifferentBytes(t *testing.T) {
	local := map[string]localFile{
		"vecchio.txt": {Digest: digest("prima"), Size: 5},
	}
	manifest := map[string]classify.RemoteState{
		"nuovo.txt": {Kind: "FILE", Digest: digest("dopo"), Size: 4},
	}
	moves, downloads, deletes := planPullRenames(
		[]string{"nuovo.txt"}, []string{"vecchio.txt"}, local, manifest,
	)
	if len(moves) != 0 {
		t.Fatalf("byte diversi accoppiati come rinomina: %v", moves)
	}
	if len(downloads) != 1 || downloads[0] != "nuovo.txt" {
		t.Fatalf("download perso: %v", downloads)
	}
	if len(deletes) != 1 || deletes[0] != "vecchio.txt" {
		t.Fatalf("cancellazione persa: %v", deletes)
	}
}

// Identical files are common (copies): one local file must serve one download,
// never two, or the second destination would be missing its bytes.
func TestPlanPullRenamesUsesEachFileOnce(t *testing.T) {
	local := map[string]localFile{
		"a.txt": {Digest: digest("uguale"), Size: 6},
	}
	manifest := map[string]classify.RemoteState{
		"b.txt": {Kind: "FILE", Digest: digest("uguale"), Size: 6},
		"c.txt": {Kind: "FILE", Digest: digest("uguale"), Size: 6},
	}
	moves, downloads, deletes := planPullRenames(
		[]string{"b.txt", "c.txt"}, []string{"a.txt"}, local, manifest,
	)
	if len(moves) != 1 || moves[0].Src != "a.txt" || moves[0].Dst != "b.txt" {
		t.Fatalf("atteso un solo spostamento (a -> b), trovato %v", moves)
	}
	if len(downloads) != 1 || downloads[0] != "c.txt" {
		t.Fatalf("il secondo file deve essere scaricato: %v", downloads)
	}
	if len(deletes) != 0 {
		t.Fatalf("la sorgente usata non deve essere cancellata: %v", deletes)
	}
}

// The same digest with a different size is not the same file (a truncated copy).
func TestPlanPullRenamesChecksSizeToo(t *testing.T) {
	local := map[string]localFile{
		"vecchio.txt": {Digest: digest("x"), Size: 100},
	}
	manifest := map[string]classify.RemoteState{
		"nuovo.txt": {Kind: "FILE", Digest: digest("x"), Size: 99},
	}
	moves, _, _ := planPullRenames([]string{"nuovo.txt"}, []string{"vecchio.txt"}, local, manifest)
	if len(moves) != 0 {
		t.Fatalf("dimensioni diverse accoppiate come rinomina: %v", moves)
	}
}

// End to end through one pass: the folder renamed on the NAS is moved here, no
// byte is downloaded, and the shell of the old folder does not stay behind.
func TestSyncOnceAppliesRemoteRenameWithoutDownloading(t *testing.T) {
	cfg, store, root := setupTest(t)
	cfg.Set("delete_enabled", true, false)
	type file struct{ name, body string }
	files := []file{
		{"Progetto/sub/f1.txt", "contenuto 1"},
		{"Progetto/sub/f2.txt", "contenuto 2"},
		{"Progetto/radice.txt", "alla radice"},
	}
	manifestText := "NASBOX_MANIFEST_V1\n"
	for _, item := range files {
		full := filepath.Join(root, filepath.FromSlash(item.name))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(item.body), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(full)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.RecordEntry(cfg.LocalRepositoryID(), item.name, state.EntryState{
			Digest: digest(item.body), Size: info.Size(), MTimeNS: info.ModTime().UnixNano(),
			Causal: "dev-a:1",
		}); err != nil {
			t.Fatal(err)
		}
		moved := "Progetto 2026" + item.name[len("Progetto"):]
		manifestText += manifestLine(moved, digest(item.body), info.Size(),
			info.ModTime().UnixNano(), "dev-b:2")
		manifestText += manifestLine(item.name, "", 0, info.ModTime().UnixNano(), "dev-b:2")
	}
	nas := &fakeNAS{config: nasConfig(), manifest: manifestText}
	transfers := &fakeTransfer{}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if result.LocalRenamed != len(files) || result.Downloads != 0 || result.LocalDeleted != 0 {
		t.Fatalf("la rinomina remota non è stata applicata in locale: %+v", result)
	}
	if len(transfers.downloaded) != 0 {
		t.Fatalf("file riscaricati invece di spostati: %v", transfers.downloaded)
	}
	for _, item := range files {
		moved := "Progetto 2026" + item.name[len("Progetto"):]
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(moved)))
		if err != nil || string(content) != item.body {
			t.Fatalf("%s non è al nuovo nome: %v", item.body, err)
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(item.name))); !os.IsNotExist(err) {
			t.Fatalf("il vecchio nome %s è ancora qui", item.name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "Progetto")); !os.IsNotExist(err) {
		t.Fatalf("la vecchia cartella vuota è rimasta")
	}
	entries, err := store.EntrySnapshot(cfg.LocalRepositoryID())
	if err != nil {
		t.Fatal(err)
	}
	if entries["Progetto 2026/sub/f1.txt"].Digest != digest("contenuto 1") {
		t.Fatalf("baseline del nuovo nome non registrata: %+v", entries["Progetto 2026/sub/f1.txt"])
	}
	if entries["Progetto/sub/f1.txt"].Digest != "" {
		t.Fatalf("il vecchio nome è ancora una baseline viva: %+v", entries["Progetto/sub/f1.txt"])
	}
}

// Rows recorded before an exclusion rule existed are dead weight: nothing ever
// looks at them again, and they made two machines with identical rules report
// different counts. They are dropped once per distinct rule set.
func TestExcludedRowsArePrunedOncePerRuleSet(t *testing.T) {
	cfg, store, _ := setupTest(t)
	repo := cfg.LocalRepositoryID()
	for _, path := range []string{"progetto/build/out.bin", "progetto/nota.txt", "progetto/server.log"} {
		if err := store.RecordEntry(repo, path, state.EntryState{Digest: digest(path), Size: 3, MTimeNS: 1}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := pruneExcludedOnce(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("attese 2 voci escluse rimosse, ottenute %d", removed)
	}
	entries, err := store.EntrySnapshot(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries["progetto/nota.txt"]; !ok {
		t.Fatal("la voce non esclusa è stata rimossa")
	}
	if _, ok := entries["progetto/build/out.bin"]; ok {
		t.Fatal("la voce esclusa è rimasta")
	}

	// Same rules again: nothing to do (the marker makes it a no-op).
	again, err := pruneExcludedOnce(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("la potatura è stata ripetuta: %d", again)
	}

	// A new rule set runs once more.
	cfg.Set("exclude_patterns", []any{"progetto/nota.txt"}, false)
	third, err := pruneExcludedOnce(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if third != 1 {
		t.Fatalf("con la nuova regola attesa 1 voce, ottenute %d", third)
	}
	entries, err = store.EntrySnapshot(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("stato non vuoto: %v", entries)
	}
}

// A local file whose path has no row in the NAS manifest used to be ignored
// forever: the baseline said "synced once", the planner trusted a remote
// deletion that never happened, and the loop only handled uploads. It exists
// only here, so it must be uploaded (the NAS adoption path makes that free when
// the bytes are already on its disk).
func TestSyncOnceUploadsFilesTheNasNeverHeardOf(t *testing.T) {
	cfg, store, root := setupTest(t)
	cfg.Set("delete_enabled", true, false)
	body := "solo qui"
	if err := os.WriteFile(filepath.Join(root, "LoreKeep", "AGENTS.md"), nil, 0o600); err != nil {
		if err := os.MkdirAll(filepath.Join(root, "LoreKeep"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "LoreKeep", "AGENTS.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(filepath.Join(root, "LoreKeep", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Live baseline, as left by an import or a NAS that lost the row.
	if err := store.RecordEntry(cfg.LocalRepositoryID(), "LoreKeep/AGENTS.md", state.EntryState{
		Digest: digest(body), Size: info.Size(), MTimeNS: info.ModTime().UnixNano(),
	}); err != nil {
		t.Fatal(err)
	}
	nas := &fakeNAS{config: nasConfig(), manifest: "NASBOX_MANIFEST_V1\n"}
	transfers := &fakeTransfer{}
	result, err := SyncOnce(cfg, store, nas, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if result.Uploaded != 1 {
		t.Fatalf("file sconosciuto al NAS non caricato: %+v (staged=%v)",
			result, transfers.stagedPaths)
	}
	if len(transfers.stagedPaths) != 1 || transfers.stagedPaths[0] != "LoreKeep/AGENTS.md" {
		t.Fatalf("percorso caricato errato: %v", transfers.stagedPaths)
	}
}
