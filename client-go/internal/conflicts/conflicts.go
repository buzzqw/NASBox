// Package conflicts finds and resolves NASBox conflict copies.
// Naming is identical to the Python client so both clients see the same groups.
package conflicts

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var conflictRE = regexp.MustCompile(`^(.*) \(conflitto da ([^ )]+) [^)]+\)(\.[^/]*)?$`)

type Group struct {
	Original   string
	Candidates []string
}

func originalName(name string) (string, bool) {
	match := conflictRE.FindStringSubmatch(name)
	if match == nil {
		return "", false
	}
	return match[1] + match[3], true
}

// Scan walks the local tree and groups conflict copies by their original path.
func Scan(root string, excluded func(string) bool) ([]Group, error) {
	groups := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if excluded != nil && excluded(rel) {
			return nil
		}
		original, ok := originalName(entry.Name())
		if !ok {
			return nil
		}
		originalRel := filepath.ToSlash(filepath.Join(filepath.Dir(rel), original))
		groups[originalRel] = append(groups[originalRel], rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]Group, 0, len(groups))
	for original, candidates := range groups {
		sort.Strings(candidates)
		result = append(result, Group{Original: original, Candidates: candidates})
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Original) < strings.ToLower(result[j].Original)
	})
	return result, nil
}

func newRunTS() string {
	now := time.Now().UTC()
	return now.Format("2006-01-02") + "--" +
		strings.Replace(now.Format("15-04-05.000000"), ".", "-", 1) + "Z"
}

func localTrashDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "sync-daemon", "trash"), nil
}

func moveToTrash(root, relative string) error {
	trashDir, err := localTrashDir()
	if err != nil {
		return err
	}
	source := filepath.Join(root, filepath.FromSlash(relative))
	destination := filepath.Join(trashDir, filepath.FromSlash(relative)+"-"+newRunTS())
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	return os.Rename(source, destination)
}

// Resolve keeps `chosen` (either the original path or one candidate) and moves
// every other version into the local trash. Moves are atomic renames.
func Resolve(root string, group Group, chosen string) error {
	valid := chosen == group.Original
	for _, candidate := range group.Candidates {
		if candidate == chosen {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("la versione scelta non e' piu' disponibile")
	}
	if chosen != group.Original {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(group.Original))); err == nil {
			if err := moveToTrash(root, group.Original); err != nil {
				return fmt.Errorf("impossibile conservare l'originale: %w", err)
			}
		}
		if err := os.Rename(
			filepath.Join(root, filepath.FromSlash(chosen)),
			filepath.Join(root, filepath.FromSlash(group.Original)),
		); err != nil {
			return err
		}
	}
	for _, candidate := range group.Candidates {
		if candidate == chosen {
			continue
		}
		if err := moveToTrash(root, candidate); err != nil {
			return fmt.Errorf("impossibile spostare nello storico %s: %w", candidate, err)
		}
	}
	return nil
}

// KeepBoth preserves the chosen candidate under a new deterministic name and
// keeps the original untouched. It is the non-destructive resolution mode.
func KeepBoth(root string, group Group, chosen string) (string, error) {
	if chosen == group.Original {
		return "", fmt.Errorf("per tenere entrambe scegli una copia conflitto")
	}
	found := false
	for _, candidate := range group.Candidates {
		if candidate == chosen {
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("la versione scelta non e' piu' disponibile")
	}
	source := filepath.Join(root, filepath.FromSlash(chosen))
	target := source + ".kept"
	for index := 1; ; index++ {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		target = fmt.Sprintf("%s.kept-%d", source, index)
	}
	if err := os.Rename(source, target); err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(relative), nil
}

// Create makes a conflict copy for a remote version that lost against a local
// edit. The suffix matches the Python client's convention.
func Create(root, originalRelative, originDevice, digest string) (string, error) {
	base := filepath.Base(originalRelative)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	short := digest
	if len(short) > 8 {
		short = short[:8]
	}
	name := fmt.Sprintf("%s (conflitto da %s %s)%s", stem, originDevice, short, ext)
	relative := filepath.ToSlash(filepath.Join(filepath.Dir(originalRelative), name))
	destination := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", err
	}
	source := filepath.Join(root, filepath.FromSlash(originalRelative))
	content, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	return relative, os.WriteFile(destination, content, 0o600)
}
