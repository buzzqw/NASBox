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
