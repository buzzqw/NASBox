package serverd

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Allineamento tra manifest e disco (porta in Go di --audit-orphans,
// --audit-adopt, --audit-phantoms e --journal-forget dello script bash).
//
// Un file può arrivare nella share senza una transazione del journal (ripristino
// da backup, copia a mano, crash prima dell'append): il manifest lo ignora, nessun
// client lo scarica e le cancellazioni non si tracciano. Il caso inverso è una
// riga viva senza file. Queste verifiche li trovano e, a richiesta, li riparano
// scrivendo solo il journal: nessun byte si sposta.

const (
	auditDevice       = "audit"
	maintenanceDevice = "maintenance"
	// Un file scritto da pochi minuti può essere un trasferimento in corso.
	auditSettleSeconds = 120
)

// Componenti interni a qualsiasi profondità: una ".sync-trash" annidata è
// interna quanto quella nella radice e i client rifiutano quei percorsi.
var auditInternalComponents = map[string]bool{
	".sync-trash": true, ".nasbox-staging": true, ".sync-partial": true,
	".nasbox-client-update": true, ".nasbox-client-update-go": true,
	"@eaDir": true, ".nasbox-root": true,
}

// rsync prepara il nuovo contenuto in ".<nome>.<6 caratteri>" accanto al
// bersaglio: adottarlo significherebbe registrare byte che stanno per sparire.
var rsyncTempName = regexp.MustCompile(`^\..+\.[A-Za-z0-9]{6}$`)

func auditPathIgnored(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if auditInternalComponents[part] {
			return true
		}
	}
	return rsyncTempName.MatchString(filepath.Base(rel))
}

// liveManifestPaths returns the encoded paths of live rows (a 64-hex digest).
func (c *Context) liveManifestPaths() (map[string]bool, error) {
	rows, err := c.renderManifest()
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(rows))
	for _, row := range rows {
		fields := strings.Split(row, "\t")
		if len(fields) > 1 && len(fields[1]) == 64 {
			live[fields[0]] = true
		}
	}
	return live, nil
}

// orphanPaths lists regular files on disk with no live manifest row, sorted.
// Paths are compared encoded, as the manifest stores them, so a name with a
// newline remains a single entry.
func (c *Context) orphanPaths() ([]string, error) {
	live, err := c.liveManifestPaths()
	if err != nil {
		return nil, err
	}
	orphans := []string{}
	walkErr := filepath.WalkDir(c.ShareRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == c.ShareRoot {
			return nil
		}
		if entry.IsDir() {
			if auditInternalComponents[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(c.ShareRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if auditPathIgnored(rel) || live[encodeField(rel)] {
			return nil
		}
		orphans = append(orphans, rel)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(orphans, func(i, j int) bool { return encodeField(orphans[i]) < encodeField(orphans[j]) })
	return orphans, nil
}

func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

func auditTxID(kind string) string {
	return fmt.Sprintf("%s-%s-%d", kind, webRunTS(), os.Getpid())
}

func cmdAuditOrphans(ctx *Context, args []string) error {
	orphans, err := ctx.orphanPaths()
	if err != nil {
		return fmt.Errorf("audit-orphans: scansione fallita: %w", err)
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	fmt.Fprintf(out, "ORPHANS_V1\nCOUNT=%d\n\x00", len(orphans))
	if hasFlag(args, "--list") {
		for _, path := range orphans {
			fmt.Fprint(out, path, "\x00")
		}
		fmt.Fprint(out, "\x00")
	}
	return nil
}

type adoptRecord struct {
	path, digest string
	size         int64
	mtimeNS      int64
}

// hashParallelism follows the NAS capabilities (cores, at most 8).
func hashParallelism(ctx *Context) int {
	if raw := strings.Fields(ctx.Config["FILE_STATES_PARALLELISM"]); len(raw) > 0 {
		if n, err := strconv.Atoi(raw[0]); err == nil && n > 0 && n <= 64 {
			return n
		}
	}
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

// adoptCandidates hashes the settled orphans in parallel.
func (c *Context) adoptCandidates(orphans []string, now time.Time) []adoptRecord {
	type job struct {
		index int
		path  string
	}
	results := make([]*adoptRecord, len(orphans))
	jobs := make(chan job)
	var wg sync.WaitGroup
	for worker := 0; worker < hashParallelism(c); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				full := filepath.Join(c.ShareRoot, filepath.FromSlash(item.path))
				before, err := os.Lstat(full)
				if err != nil || !before.Mode().IsRegular() ||
					now.Sub(before.ModTime()) < auditSettleSeconds*time.Second {
					continue
				}
				digest, err := sha256File(full)
				if err != nil {
					continue
				}
				// Se il file è cambiato mentre lo leggevo il digest non è affidabile.
				after, err := os.Lstat(full)
				if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
					continue
				}
				results[item.index] = &adoptRecord{
					path: item.path, digest: digest, size: after.Size(), mtimeNS: after.ModTime().UnixNano(),
				}
			}
		}()
	}
	for index, path := range orphans {
		jobs <- job{index, path}
	}
	close(jobs)
	wg.Wait()
	records := make([]adoptRecord, 0, len(orphans))
	for _, record := range results {
		if record != nil {
			records = append(records, *record)
		}
	}
	return records
}

func cmdAuditAdopt(ctx *Context, args []string) error {
	orphans, err := ctx.orphanPaths()
	if err != nil {
		return fmt.Errorf("audit-adopt: scansione fallita: %w", err)
	}
	records := ctx.adoptCandidates(orphans, time.Now())
	if len(records) == 0 {
		fmt.Println("ADOPTED=0")
		return nil
	}
	if hasFlag(args, "--dry-run") {
		fmt.Printf("ADOPTED=0\nWOULD_ADOPT=%d\n", len(records))
		return nil
	}
	ts := journalNow()
	rows := make([]string, 0, len(records))
	for _, record := range records {
		rows = append(rows, journalRowPUT(encodeField(record.path), record.digest,
			strconv.FormatInt(record.size, 10), strconv.FormatInt(record.mtimeNS, 10),
			encodeField(auditDevice), ts, ""))
	}
	if err := ctx.appendTransaction(auditTxID("audit-adopt"), auditDevice, rows); err != nil {
		fmt.Println("ADOPTED=0\nERROR=journal non aggiornato")
		return err
	}
	fmt.Printf("ADOPTED=%d\n", len(records))
	return nil
}

// phantomPaths lists live rows whose file is gone. A path that exists as a
// directory or a link is a shape the sync never wrote: left to a human.
func (c *Context) phantomPaths() ([]string, error) {
	rows, err := c.renderManifest()
	if err != nil {
		return nil, err
	}
	phantoms := []string{}
	for _, row := range rows {
		fields := strings.Split(row, "\t")
		if len(fields) < 2 || len(fields[1]) != 64 || internalPath.MatchString(fields[0]) {
			continue
		}
		path := decodeField(fields[0])
		if _, err := os.Lstat(filepath.Join(c.ShareRoot, filepath.FromSlash(path))); err == nil {
			continue
		}
		phantoms = append(phantoms, path)
	}
	return phantoms, nil
}

func cmdAuditPhantoms(ctx *Context, args []string) error {
	phantoms, err := ctx.phantomPaths()
	if err != nil {
		return err
	}
	forget := hasFlag(args, "--forget")
	if forget && len(phantoms) > 0 {
		if hasFlag(args, "--dry-run") {
			fmt.Printf("PHANTOMS=%d\nWOULD_FORGET=%d\n", len(phantoms), len(phantoms))
			return nil
		}
		rows := deleteRows(phantoms, auditDevice, journalNow())
		if err := ctx.appendTransaction(auditTxID("audit-phantoms"), auditDevice, rows); err != nil {
			fmt.Printf("PHANTOMS=%d\nERROR=journal non aggiornato\n", len(phantoms))
			return err
		}
	}
	fmt.Printf("PHANTOMS=%d\n", len(phantoms))
	if !forget {
		for index, path := range phantoms {
			if index >= 20 {
				break
			}
			fmt.Println(path)
		}
	}
	return nil
}

// validForgetPath is validRelative without the internal-folder rule: forgetting
// a path the clients refuse is exactly the point. Absolute paths, "." / ".."
// components and symlinked ancestors stay refused.
func (c *Context) validForgetPath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	current := c.ShareRoot
	parts := strings.Split(path, "/")
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

// cmdJournalForget tombstones the NUL-separated paths read from stdin. Only the
// journal is written: nothing on disk moves, so a version in .sync-trash stays.
func cmdJournalForget(ctx *Context) error {
	reader := bufio.NewReader(os.Stdin)
	paths := []string{}
	for {
		frame, err := reader.ReadString('\x00')
		if len(frame) > 0 {
			path := strings.TrimSuffix(frame, "\x00")
			if ctx.validForgetPath(path) {
				paths = append(paths, path)
			}
		}
		if err != nil {
			break
		}
	}
	if len(paths) == 0 {
		fmt.Println("FORGOTTEN=0")
		return nil
	}
	rows := deleteRows(paths, maintenanceDevice, journalNow())
	if err := ctx.appendTransaction(auditTxID("journal-forget"), maintenanceDevice, rows); err != nil {
		fmt.Println("FORGOTTEN=0\nERROR=journal non aggiornato")
		return err
	}
	fmt.Printf("FORGOTTEN=%d\n", len(paths))
	return nil
}
