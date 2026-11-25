package engine

import (
	"testing"

	"nasbox-go/internal/config"
)

// The exclusions that used to differ between the two PCs. They live in the code
// now: the same repository must look the same on every machine, or the tracked
// counts (and the sync decisions) diverge for no reason.
func TestSharedExclusionsCoverBuildOutputOnEveryClient(t *testing.T) {
	cfg := config.New("")
	for _, path := range []string{
		"progetto/build/out.bin",
		"progetto/dist/app.tar.gz",
		"progetto/target/classi/A.class",
		"progetto/.gradle/caches/x.bin",
		"progetto/.idea/workspace.xml",
		"progetto/.vscode/settings.json",
		"progetto/.next/cache/x",
		"progetto/.cache/y",
		"progetto/modulo.o",
		"progetto/modulo.obj",
		"progetto/server.log",
		"progetto/build.out",
	} {
		if !excluded(cfg, path) {
			t.Fatalf("percorso non escluso: %s", path)
		}
	}
	// User content with similar names must still be synced.
	for _, path := range []string{
		"progetto/build-guide.md",
		"progetto/distribuzione/nota.txt",
		"progetto/targets.yaml",
		"progetto/loghi/logo.png",
		"progetto/cache-privata.txt",
		"progetto/outline.tex",
	} {
		if excluded(cfg, path) {
			t.Fatalf("contenuto utente escluso per errore: %s", path)
		}
	}
}
