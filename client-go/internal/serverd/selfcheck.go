package serverd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// cmdSelfCheck risponde a una domanda: questo NAS è in uno stato in cui una
// sincronizzazione pianificata può riuscire? È pensato per un watchdog esterno
// (attività DSM) e per la diagnostica, quindi ogni riga è leggibile da script e
// l'exit code è non-zero quando serve attenzione.
//
// Con --quick salta il controllo del journal: aprire l'indice SQLite in WAL
// tocca il disco e, eseguito ogni pochi minuti, impedirebbe l'ibernazione degli
// hard disk. Il controllo completo resta disponibile e va eseguito di rado.
func cmdSelfCheck(ctx *Context, args []string) error {
	quick := hasFlag(args, "--quick")
	failures := 0
	report := func(name string, ok bool, detail string) {
		status := "ok"
		if !ok {
			status = "fail"
			failures++
		}
		if detail == "" {
			fmt.Printf("CHECK_%s=%s\n", name, status)
			return
		}
		fmt.Printf("CHECK_%s=%s %s\n", name, status, detail)
	}

	p := ctx.paths()
	interval := parseConfigInt(ctx.Config["CHECK_INTERVAL_MINUTES"], 60)

	_, running := ctx.daemonRunning()
	report("DAEMON", running, daemonDetail(running))

	if info, err := os.Stat(ctx.ShareRoot); err != nil || !info.IsDir() {
		report("SHARE_ROOT", false, ctx.ShareRoot)
	} else {
		report("SHARE_ROOT", true, "")
	}

	if quick {
		report("JOURNAL", true, "saltato (--quick)")
	} else {
		writeErr := ctx.withJournalLock(func() error {
			index, err := openMetadataIndex(p)
			if err != nil {
				return err
			}
			defer index.Close()
			return index.syncLocked()
		})
		report("JOURNAL", writeErr == nil, errorDetail(writeErr))
	}

	if running {
		age := fileAge(p.LastRun)
		limit := time.Duration(interval) * time.Minute * 2
		if limit < 2*time.Hour {
			limit = 2 * time.Hour
		}
		if age < 0 {
			report("LAST_RUN", false, "last-run assente")
		} else {
			report("LAST_RUN", age <= limit, "età "+age.Round(time.Minute).String())
		}
	}

	minFree := parseConfigInt(ctx.Config["SELF_CHECK_MIN_FREE_MB"], 512)
	for _, item := range []struct{ name, path string }{
		{"DISK_STATE", p.Dir},
		{"DISK_SHARE", ctx.ShareRoot},
	} {
		free, err := diskFreeMB(item.path)
		if err != nil {
			report(item.name, false, err.Error())
			continue
		}
		report(item.name, free >= minFree, fmt.Sprintf("%d MB liberi (minimo %d)", free, minFree))
	}

	quota := parseConfigInt(ctx.Config["CHUNK_MAX_MB"], 0) * 1024 * 1024
	used := parseConfigInt(firstField(readString(filepath.Join(p.Dir, "chunk-usage"))), 0)
	if quota > 0 {
		percent := used * 100 / quota
		report("CHUNK_QUOTA", percent < 90, fmt.Sprintf("%d%% usato", percent))
	}

	if holder, _, stale, idle := ctx.leaseState(time.Now()); stale {
		report("LEASE", false, fmt.Sprintf("%s/%s inattivo da %s", holder.Device, holder.Host, idle.Round(time.Minute)))
	} else {
		report("LEASE", true, "")
	}

	temporary := staleTemporaryDirs(p.Dir, time.Duration(parseConfigInt(ctx.Config["STATE_TEMP_MAX_AGE_HOURS"], 24))*time.Hour*2)
	report("TEMP_DIRS", len(temporary) == 0, strings.Join(temporary, ","))

	failing := failures > 0
	if failing {
		fmt.Printf("SELF_CHECK=fail problems=%d\n", failures)
		return exitCode(1)
	}
	fmt.Println("SELF_CHECK=ok")
	return nil
}

func daemonDetail(running bool) string {
	if running {
		return ""
	}
	return "demone non in esecuzione"
}

func errorDetail(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func fileAge(path string) time.Duration {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return time.Since(info.ModTime())
}

func diskFreeMB(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize) / (1024 * 1024), nil
}

// staleTemporaryDirs lists disposable state directories that survived far
// beyond the maintenance cutoff, which usually means the daemon stopped.
func staleTemporaryDirs(dir string, limit time.Duration) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	found := make([]string, 0, 2)
	cutoff := time.Now().Add(-limit)
	for _, entry := range entries {
		if !entry.IsDir() || !temporaryStateEntry(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			found = append(found, entry.Name())
		}
	}
	return found
}
