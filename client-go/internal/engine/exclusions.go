package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"nasbox-go/internal/config"
	"nasbox-go/internal/state"
)

// The exclusion rules decide what a client ever looks at. They now come from one
// place (systemExcludes in the code, plus an optional per-client list), but the
// rows recorded *before* a rule existed stay in the database forever, because
// nothing evaluates them again. Two machines with the same rules therefore kept
// reporting different numbers -- and those numbers were the only visible sign of
// a real gap in the tracked set.
//
// So: when the effective rules change (a new release, an edited config), the
// rows they now ignore are dropped once. Pruning is safe: an entry for an
// excluded path cannot be planned, uploaded or deleted, and if the path is
// un-excluded later the client simply looks at it again (adopting it from the
// NAS manifest when the bytes are already there).
func exclusionFingerprint(cfg *config.Config) string {
	parts := append([]string{}, systemExcludes...)
	parts = append(parts, stringValues(cfg, "exclude_patterns")...)
	parts = append(parts, stringValues(cfg, "selective_sync_excludes")...)
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// pruneExcludedOnce applies the rule set to the stored state, at most once per
// distinct rule set. It returns how many paths were dropped.
func pruneExcludedOnce(cfg *config.Config, store *state.Store) (int, error) {
	fingerprint := exclusionFingerprint(cfg)
	if previous, err := store.String("exclusions:pruned"); err == nil && previous == fingerprint {
		return 0, nil
	}
	removed, err := store.PruneExcluded(cfg.LocalRepositoryID(), func(path string) bool {
		return excluded(cfg, path)
	})
	if err != nil {
		return 0, err
	}
	if err := store.SetString("exclusions:pruned", fingerprint); err != nil {
		return removed, err
	}
	return removed, nil
}
