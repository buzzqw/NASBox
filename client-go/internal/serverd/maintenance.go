package serverd

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type trashVersion struct {
	path    string
	size    int64
	modTime time.Time
}

// cmdRunOnce esegue il passo di manutenzione essenziale: quota dello storico,
// registro dell'ultimo passo e report di utilizzo. Le rotazioni dei log e la
// pulizia dei temporanei arrivano nella milestone 3.
// maintainStateFiles esegue la manutenzione limitata al piccolo stato del
// demone: rotazione di log/eventi e rimozione dei temporanei vecchi.
func (c *Context) maintainStateFiles() {
	p := c.paths()
	for _, item := range []struct {
		path  string
		limit int64
	}{
		{p.Events, parseConfigInt(c.Config["EVENTS_MAX_BYTES"], 5242880)},
		{p.ServerLog, parseConfigInt(c.Config["LOG_MAX_BYTES"], 5242880)},
	} {
		info, err := os.Stat(item.path)
		if err == nil && info.Size() > item.limit && item.limit > 0 {
			_ = os.Rename(item.path, item.path+".1")
		}
	}
	hours := parseConfigInt(c.Config["STATE_TEMP_MAX_AGE_HOURS"], 24)
	if hours <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		temporary := strings.Contains(name, ".rollback.") ||
			strings.HasPrefix(name, "prune-files.") || strings.HasPrefix(name, "prune-dirs.") ||
			strings.HasPrefix(name, "prune-staging.") || strings.HasPrefix(name, "fs-want.") ||
			strings.HasPrefix(name, "fs-match.") || strings.HasPrefix(name, "manifest.tsv.tmp.") ||
			strings.HasPrefix(name, "manifest.tsv.sweep.") || strings.HasPrefix(name, "transfer-journal.tsv.append.") ||
			strings.HasPrefix(name, "transfer-journal.tsv.delete.") ||
			strings.HasPrefix(name, "transfer-journal.tsv.rollback.") || strings.HasPrefix(name, "restore-manifest.")
		if !temporary {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(p.Dir, name))
		}
	}
}

func parseConfigInt(raw string, fallback int64) int64 {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return fallback
	}
	value, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return fallback
	}
	return value
}

func cmdRunOnce(ctx *Context) error {
	p := ctx.paths()
	trash := filepath.Join(ctx.ShareRoot, ".sync-trash")
	versions := []trashVersion{}
	_ = filepath.Walk(trash, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			versions = append(versions, trashVersion{path: path, size: info.Size(), modTime: info.ModTime()})
		}
		return nil
	})
	used := int64(0)
	for _, version := range versions {
		used += version.size
	}
	limitMB, _ := strconv.ParseInt(strings.Fields(value(ctx.Config, "TRASH_MAX_MB", "0"))[0], 10, 64)
	keepDays, _ := strconv.ParseInt(strings.Fields(value(ctx.Config, "TRASH_MIN_KEEP_DAYS", "7"))[0], 10, 64)
	retentionDays, _ := strconv.ParseInt(strings.Fields(value(ctx.Config, "RETENTION_DAYS", "30"))[0], 10, 64)
	if retentionDays > 0 {
		cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
		kept := versions[:0]
		for _, version := range versions {
			if version.modTime.Before(cutoff) {
				_ = os.Remove(version.path)
				used -= version.size
				continue
			}
			kept = append(kept, version)
		}
		versions = kept
	}
	if limitMB > 0 {
		sort.Slice(versions, func(i, j int) bool { return versions[i].modTime.Before(versions[j].modTime) })
		limit := limitMB * 1024 * 1024
		now := time.Now()
		for _, version := range versions {
			if used <= limit {
				break
			}
			if now.Sub(version.modTime) < time.Duration(keepDays)*24*time.Hour {
				continue
			}
			if err := os.Remove(version.path); err != nil {
				continue
			}
			used -= version.size
		}
	}
	ctx.maintainStateFiles()
	ctx.maintainChunks()
	ctx.backupState()
	ctx.maintainAudit()
	ctx.maintainNotifications()
	now := strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(p.LastRun, []byte(now+"\n"), 0o600); err != nil {
		return err
	}
	files := 0
	for _, version := range versions {
		if _, err := os.Stat(version.path); err == nil {
			files++
		}
	}
	return os.WriteFile(p.TrashUsage, []byte(strconv.FormatInt(used, 10)+" "+strconv.Itoa(files)+" "+now+"\n"), 0o600)
}
