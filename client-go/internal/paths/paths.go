// Package paths centralises the no-traversal/no-symlink rules used before any
// local or remote filesystem mutation.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var internalDirs = map[string]bool{
	".sync-trash": true, ".sync-partial": true, ".nasbox-staging": true,
	".nasbox-root": true, "@eaDir": true,
}

// ValidateRelative rejects absolute paths, traversal and NASBox internals.
func ValidateRelative(relative string) error {
	if strings.TrimSpace(relative) == "" {
		return fmt.Errorf("percorso vuoto")
	}
	if filepath.IsAbs(relative) || strings.HasPrefix(relative, "/") {
		return fmt.Errorf("percorso assoluto non consentito: %s", relative)
	}
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("componente di percorso non valido: %s", relative)
		}
		if internalDirs[part] {
			return fmt.Errorf("percorso interno NASBox non consentito: %s", relative)
		}
	}
	return nil
}

// SafeDestination returns root/relative after ensuring no existing component is
// a symlink. The final target may exist, but it must not be a symlink either.
func SafeDestination(root, relative string) (string, error) {
	if err := ValidateRelative(relative); err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(rootAbs, filepath.FromSlash(relative))
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("percorso fuori dalla root: %s", relative)
	}
	current := rootAbs
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink nel percorso di destinazione: %s", relative)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("componente non-cartella nel percorso: %s", relative)
		}
	}
	return target, nil
}
