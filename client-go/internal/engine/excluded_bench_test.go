package engine

import (
	"fmt"
	pathpkg "path"
	"path/filepath"
	"strings"
	"testing"

	"nasbox-go/internal/config"
)

func benchPaths() []string {
	paths := make([]string, 0, 120000)
	for i := 0; i < 120000; i++ {
		paths = append(paths, fmt.Sprintf("Documenti/Progetti/cliente-%d/anno-%d/sotto/file-%d.pdf", i%300, i%7, i))
	}
	return paths
}

func benchConfig() *config.Config {
	cfg := config.New("")
	cfg.Set("exclude_patterns", []any{"venv", ".venv", "__pycache__", "node_modules", "*.pyc", "*.pyo", ".git/",
		"node_modules/", ".venv/", "venv/", "__pycache__/", "build/", "dist/", "target/", ".gradle/", ".idea/",
		".vscode/", ".next/", ".cache/", "*.class", "*.o", "*.obj", "sync-daemon/"}, false)
	return cfg
}

func BenchmarkExcluded120k(b *testing.B) {
	cfg, paths := benchConfig(), benchPaths()
	for n := 0; n < b.N; n++ {
		for _, path := range paths {
			excluded(cfg, path)
		}
	}
}

// excludedReference is the pre-1.1.0 implementation, kept as the oracle.
func excludedReference(cfg *config.Config, path string) bool {
	normalized := filepath.ToSlash(path)
	parts := strings.Split(normalized, "/")
	for _, part := range parts {
		if internalDirNames[part] {
			return true
		}
	}
	for _, folder := range stringValues(cfg, "selective_sync_excludes") {
		folder = strings.Trim(strings.TrimSpace(folder), "/")
		if folder != "" && (normalized == folder || strings.HasPrefix(normalized, folder+"/")) {
			return true
		}
	}
	for _, raw := range append(append([]string{}, systemExcludes...), stringValues(cfg, "exclude_patterns")...) {
		pattern := strings.TrimSuffix(strings.TrimSpace(strings.ReplaceAll(raw, `\`, "/")), "/")
		if pattern == "" {
			continue
		}
		if matched, _ := pathpkg.Match(pattern, normalized); matched {
			return true
		}
		if !strings.Contains(pattern, "/") {
			for _, part := range parts {
				if matched, _ := pathpkg.Match(pattern, part); matched {
					return true
				}
			}
		}
	}
	return false
}

func TestCompiledExcludeMatchesReference(t *testing.T) {
	cfg := benchConfig()
	cfg.Set("selective_sync_excludes", []any{"Foto", "Documenti/Privato"}, false)
	cfg.Set("exclude_patterns", append(cfg.Get("exclude_patterns").([]any),
		"*conflitto\\ da\\ *", "build/out/*.tmp", "Lavoro/cache", "f[0-9].log", "~$*", "*.bak*"), false)
	names := []string{"a.txt", "venv", "x.pyc", "Thumbs.db", ".DS_Store", "~$doc.docx", ".~lock.a#",
		"f1.log", "fa.log", "build", "out", "cache", "nota.bak", "nota.bak2", "a.tmp.1", "file.part",
		"report.aux", "__pycache__", ".git", "node_modules", "Privato", "Foto", "Lavoro", "doc conflitto da pc"}
	paths := []string{}
	for _, a := range names {
		paths = append(paths, a)
		for _, b := range names {
			paths = append(paths, a+"/"+b)
			paths = append(paths, "Documenti/"+a+"/"+b)
		}
	}
	paths = append(paths, "build/out/x.tmp", "Lavoro/cache", "Lavoro/cache/x", "Documenti/Privato/a", "Documenti/Privato")
	for _, path := range paths {
		if got, want := excluded(cfg, path), excludedReference(cfg, path); got != want {
			t.Fatalf("excluded(%q) = %v, reference %v", path, got, want)
		}
	}
}
