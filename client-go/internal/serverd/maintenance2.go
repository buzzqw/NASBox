package serverd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Manutenzione notturna aggiuntiva, eseguita da --run-once insieme a retention e
// pulizia dei log: garbage collection dei chunk, verifica periodica
// d'integrità e backup dello stato che non si ricostruisce.
//
// Chiavi di server.conf (tutte opzionali):
//
//	CHUNK_GC=true              GC dei chunk a ogni passo (false per disattivarla)
//	CHUNK_GC_GRACE_HOURS=24    periodo di grazia prima di rimuovere un chunk
//	CHUNK_MAX_MB=0             quota dello store chunk (0 = nessun limite)
//	CHUNK_FSCK_DAYS=7          ogni quanti giorni rileggere tutti i chunk (0 = mai)
//	CHUNK_SCRUB_HOUR=3         prima ora della finestra di manutenzione
//	CHUNK_SCRUB_WINDOW_HOURS=0 durata della finestra (0 = sempre, 5 = solo di notte)
//	CHUNK_SCRUB_NICE=10        priorità CPU durante GC e fsck (0 = normale)
//	STATE_BACKUP_KEEP=7        copie giornaliere di journal e manifest (0 = nessuna)
//	AUDIT_AUTO=report          report (default) | adopt (adotta i file orfani) | off

// logEvent appends one JSON line to events.jsonl. Errors are ignored: an event
// must never make maintenance fail.
func (c *Context) logEvent(kind string, fields map[string]any) {
	entry := map[string]any{"ts": time.Now().Unix(), "source": "server", "event": kind}
	for key, value := range fields {
		entry[key] = value
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	file, err := os.OpenFile(c.paths().Events, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(line, '\n'))
}

func firstField(raw string) string {
	if fields := strings.Fields(raw); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// stampOlderThan reports whether the stamp file is missing or older than age.
func stampOlderThan(path string, age time.Duration) bool {
	info, err := os.Stat(path)
	return err != nil || time.Since(info.ModTime()) >= age
}

func touchStamp(path string) { _ = os.WriteFile(path, []byte(journalNow()+"\n"), 0o600) }

func (c *Context) inScrubWindow() bool {
	window := int(parseConfigInt(c.Config["CHUNK_SCRUB_WINDOW_HOURS"], 0))
	if window <= 0 || window >= 24 {
		return true // default: compatibile con il comportamento storico
	}
	start := int(parseConfigInt(c.Config["CHUNK_SCRUB_HOUR"], 3)) % 24
	hour := c.now().Hour()
	for offset := 0; offset < window; offset++ {
		if hour == (start+offset)%24 {
			return true
		}
	}
	return false
}

func (c *Context) maintainChunks() {
	if strings.EqualFold(firstField(c.Config["CHUNK_GC"]), "false") {
		return
	}
	if !c.inScrubWindow() {
		return
	}
	nice := int(parseConfigInt(c.Config["CHUNK_SCRUB_NICE"], 10))
	store, err := c.chunkStore()
	if err != nil {
		c.logEvent("chunk_maintenance_error", map[string]any{"error": err.Error()})
		return
	}
	live, err := c.liveDigests()
	if err != nil {
		c.logEvent("chunk_maintenance_error", map[string]any{"error": err.Error()})
		return
	}
	grace := time.Duration(parseConfigInt(c.Config["CHUNK_GC_GRACE_HOURS"], 24)) * time.Hour
	maxBytes := parseConfigInt(c.Config["CHUNK_MAX_MB"], 0) * 1024 * 1024
	report, err := withLowPriority(func() (GCReport, error) {
		return store.GC(GCOptions{Live: live, Grace: grace, MaxBytes: maxBytes, Now: c.now()})
	}, nice)
	if err != nil {
		c.logEvent("chunk_gc_error", map[string]any{"error": err.Error()})
		return
	}
	if report.ObjectsRemoved > 0 || report.MetasRemoved > 0 {
		c.logEvent("chunk_gc", map[string]any{
			"objects_removed": report.ObjectsRemoved, "bytes_freed": report.BytesFreed,
			"metas_removed": report.MetasRemoved, "bytes_before": report.BytesBefore,
			"bytes_after": report.BytesAfter, "quota_triggered": maxBytes > 0,
		})
	}
	if report.QuotaBlocked {
		c.logEvent("chunk_quota_blocked", map[string]any{
			"bytes": report.BytesAfter, "quota_bytes": maxBytes,
			"reachable": report.BytesAfter,
		})
		c.notifyOnce("chunk_quota", "NASBox: quota chunk non raggiungibile",
			fmt.Sprintf("Lo store chunk usa %d byte con quota %d: tutti i chunk sono ancora referenziati. Aumenta CHUNK_MAX_MB.",
				report.BytesAfter, maxBytes))
	}
	if objects, bytes, err := store.Stats(); err == nil {
		usage := filepath.Join(c.StateDir, "chunk-usage")
		line := fmt.Sprintf("%d %d %d\n", bytes, objects, c.now().Unix())
		_ = os.WriteFile(usage, []byte(line), 0o600)
	}
	days := parseConfigInt(c.Config["CHUNK_FSCK_DAYS"], 7)
	stamp := filepath.Join(c.StateDir, "chunk-fsck.stamp")
	if days <= 0 || !stampOlderThan(stamp, time.Duration(days)*24*time.Hour) {
		return
	}
	check, err := withLowPriority(func() (FsckReport, error) {
		return store.Fsck(false)
	}, nice)
	if err != nil {
		c.logEvent("chunk_fsck_error", map[string]any{"error": err.Error()})
		return
	}
	c.recordFsck(check)
	touchStamp(stamp)
	if problems := check.Corrupt + check.MetasBroken + check.SnapshotsBad; problems > 0 {
		c.notifyOnce("fsck", "NASBox: problemi nei chunk",
			fmt.Sprintf("La verifica ha trovato %d chunk corrotti, %d meta rotti, %d problemi negli snapshot.",
				check.Corrupt, check.MetasBroken, check.SnapshotsBad))
		c.logEvent("chunk_fsck_problems", map[string]any{
			"corrupt": check.Corrupt, "metas_broken": check.MetasBroken,
			"snapshot_problems": check.SnapshotsBad, "details": check.Problems,
		})
	}
}

func copyFileSync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// backupState copies the journal and the manifest (the sources everything else,
// including the SQLite index, is rebuilt from) once a day, keeping the last N.
func (c *Context) backupState() {
	keep := int(parseConfigInt(c.Config["STATE_BACKUP_KEEP"], 7))
	if keep <= 0 {
		return
	}
	p := c.paths()
	stamp := filepath.Join(p.Dir, "state-backup.stamp")
	if !stampOlderThan(stamp, 20*time.Hour) {
		return
	}
	root := filepath.Join(p.Dir, "backup")
	target := filepath.Join(root, time.Now().UTC().Format("20060102-150405"))
	if err := os.MkdirAll(target, 0o700); err != nil {
		c.logEvent("state_backup_error", map[string]any{"error": err.Error()})
		return
	}
	err := c.withJournalLock(func() error {
		for _, src := range []string{p.Journal, p.Manifest, p.Revision} {
			if _, statErr := os.Stat(src); statErr != nil {
				continue
			}
			if err := copyFileSync(src, filepath.Join(target, filepath.Base(src))); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = os.RemoveAll(target)
		c.logEvent("state_backup_error", map[string]any{"error": err.Error()})
		return
	}
	touchStamp(stamp)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.RemoveAll(filepath.Join(root, names[0]))
		names = names[1:]
	}
}

// maintainAudit looks for manifest/disk drift once per pass. By default it only
// reports (events.jsonl); AUDIT_AUTO=adopt also registers settled orphan files,
// which is safe (no byte moves). Phantom rows are NEVER forgotten here: with a
// share that is not mounted every row would look like a phantom.
func (c *Context) maintainAudit() {
	mode := strings.ToLower(firstField(c.Config["AUDIT_AUTO"]))
	if mode == "off" {
		return
	}
	orphans, err := c.orphanPaths()
	if err != nil {
		c.logEvent("audit_error", map[string]any{"error": err.Error()})
		return
	}
	phantoms, err := c.phantomPaths()
	if err != nil {
		c.logEvent("audit_error", map[string]any{"error": err.Error()})
		return
	}
	adopted := 0
	if mode == "adopt" && len(orphans) > 0 {
		records := c.adoptCandidates(orphans, time.Now())
		if len(records) > 0 {
			ts := journalNow()
			rows := make([]string, 0, len(records))
			for _, record := range records {
				rows = append(rows, journalRowPUT(encodeField(record.path), record.digest,
					strconv.FormatInt(record.size, 10), strconv.FormatInt(record.mtimeNS, 10),
					encodeField(auditDevice), ts, ""))
			}
			if err := c.appendTransaction(auditTxID("audit-adopt"), auditDevice, rows); err != nil {
				c.logEvent("audit_error", map[string]any{"error": err.Error()})
			} else {
				adopted = len(records)
			}
		}
	}
	if len(orphans) > 0 || len(phantoms) > 0 {
		if len(phantoms) > 0 || (mode != "adopt" && len(orphans) > 0) {
			c.notifyOnce("drift", "NASBox: manifest e disco non coincidono",
				fmt.Sprintf("%d file sul disco non sono registrati, %d righe puntano a file mancanti.", len(orphans)-adopted, len(phantoms)))
		}
		c.logEvent("audit_drift", map[string]any{
			"orphans": len(orphans), "phantoms": len(phantoms), "adopted": adopted, "mode": mode,
		})
	}
}
