package serverd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Il chunk store è una cache di deduplica/trasferimento: i byte autorevoli dei
// file vivi restano nell'albero, mentre gli snapshot sono l'unica parte che
// non si ricostruisce. La GC è quindi conservativa:
//
//   - un chunk si cancella solo se nessun meta-file "vivo" né nessuno snapshot
//     lo referenzia **e** è più vecchio del periodo di grazia (un upload non
//     ancora pubblicato ha chunk ma non ancora il meta);
//   - un meta-file è "vivo" se il suo digest compare nel manifest vivo, oppure
//     se è recente;
//   - se uno snapshot o un meta recente non si legge, non si cancella nulla.
//
// `--chunk-has` aggiorna la data dei chunk che trova, così un chunk riusato da
// un client non può essere raccolto tra "ce l'hai?" e la pubblicazione.

// DefaultChunkGrace è il periodo di grazia predefinito della GC.
const DefaultChunkGrace = 24 * time.Hour

// touchInterval evita di riscrivere la data di un chunk già toccato di recente.
const touchInterval = time.Hour

// Touch segna un chunk come usato adesso. Gli errori sono ignorati: è solo una
// protezione in più contro la GC.
func (s *ChunkStore) Touch(digest string) {
	path := s.objectPath(digest)
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) < touchInterval {
		return
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

// GCOptions configura una passata di garbage collection.
type GCOptions struct {
	Live     map[string]bool // digest di file interi presenti nel manifest vivo
	Grace    time.Duration
	DryRun   bool
	Now      time.Time
	MaxBytes int64 // 0 = nessuna quota; >0 = libera fino a rientrare sotto questo limite
}

// GCReport riassume una passata di GC.
type GCReport struct {
	ObjectsScanned int
	ObjectsRemoved int
	BytesFreed     int64
	MetasScanned   int
	MetasRemoved   int
	TmpRemoved     int
	BytesBefore    int64
	BytesAfter     int64
	QuotaBlocked   bool // la quota non è raggiungibile senza toccare dati vivi
}

func isDigest(value string) bool { return digest64.MatchString(value) }

// walkObjects chiama fn per ogni oggetto del store con il suo digest.
func (s *ChunkStore) walkObjects(fn func(digest, path string, info os.FileInfo) error) error {
	root := filepath.Join(s.root, "objects")
	prefixes, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, prefix := range prefixes {
		if !prefix.IsDir() || len(prefix.Name()) != 2 {
			continue
		}
		dir := filepath.Join(root, prefix.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			digest := prefix.Name() + entry.Name()
			if !isDigest(digest) {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if err := fn(digest, filepath.Join(dir, entry.Name()), info); err != nil {
				return err
			}
		}
	}
	return nil
}

// metaFiles elenca i meta-file (digest → chunk) con la loro data.
func (s *ChunkStore) metaFiles() (map[string]os.FileInfo, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "files"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	result := make(map[string]os.FileInfo, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || !isDigest(strings.TrimSuffix(name, ".json")) {
			continue
		}
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			result[strings.TrimSuffix(name, ".json")] = info
		}
	}
	return result, nil
}

// snapshotChunks raccoglie tutti i chunk referenziati dagli snapshot. Uno
// snapshot illeggibile è un errore: senza di esso non si può dire cosa sia
// raggiungibile.
func (s *ChunkStore) snapshotChunks() (map[string]bool, error) {
	ids, err := s.ListSnapshots()
	if err != nil {
		return nil, err
	}
	marked := map[string]bool{}
	for _, id := range ids {
		snap, err := s.LoadSnapshot(id)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s illeggibile: %w", id, err)
		}
		for _, file := range snap.Files {
			for _, chunk := range file.Chunks {
				marked[chunk] = true
			}
		}
	}
	return marked, nil
}

// GC rimuove meta-file e chunk non più raggiungibili.
func (s *ChunkStore) GC(opts GCOptions) (GCReport, error) {
	var report GCReport
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	grace := opts.Grace
	if grace < 0 {
		grace = 0
	}
	old := func(info os.FileInfo) bool { return now.Sub(info.ModTime()) >= grace }

	marked, err := s.snapshotChunks()
	if err != nil {
		return report, err
	}
	metas, err := s.metaFiles()
	if err != nil {
		return report, err
	}
	var dropMetas []string
	for digest, info := range metas {
		report.MetasScanned++
		chunks, loadErr := s.LoadFileMeta(digest)
		live := opts.Live[digest]
		switch {
		case loadErr != nil && !old(info):
			return report, fmt.Errorf("meta recente %s illeggibile: %w", digest, loadErr)
		case loadErr != nil:
			dropMetas = append(dropMetas, digest) // rotto e vecchio: si rigenera
		case live || !old(info):
			for _, chunk := range chunks {
				marked[chunk] = true
			}
		default:
			dropMetas = append(dropMetas, digest)
		}
	}

	type candidate struct {
		path string
		info os.FileInfo
	}
	candidates := []candidate{}
	var totalBytes int64
	err = s.walkObjects(func(digest, path string, info os.FileInfo) error {
		report.ObjectsScanned++
		totalBytes += info.Size()
		if marked[digest] || !old(info) {
			return nil
		}
		if opts.MaxBytes > 0 {
			// With a quota the oldest unreferenced chunks go first: Touch()
			// refreshes mtime, so recent reuse protects them automatically.
			candidates = append(candidates, candidate{path: path, info: info})
			return nil
		}
		report.ObjectsRemoved++
		report.BytesFreed += info.Size()
		if opts.DryRun {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	if opts.MaxBytes > 0 {
		report.BytesBefore = totalBytes
		target := totalBytes - opts.MaxBytes
		if target > 0 {
			sort.Slice(candidates, func(i, j int) bool {
				return candidates[i].info.ModTime().Before(candidates[j].info.ModTime())
			})
			for _, item := range candidates {
				if report.BytesFreed >= target {
					break
				}
				report.ObjectsRemoved++
				report.BytesFreed += item.info.Size()
				if opts.DryRun {
					continue
				}
				if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
					return report, err
				}
			}
		}
		report.BytesAfter = totalBytes - report.BytesFreed
		report.QuotaBlocked = report.BytesFreed < target
	}
	// I meta si tolgono dopo i chunk: un'interruzione lascia al peggio un
	// meta che punta a chunk mancanti, che l'fsck segnala e `--file-chunks`
	// tratta come MISS (rigenerato dal file vivo).
	for _, digest := range dropMetas {
		report.MetasRemoved++
		if opts.DryRun {
			continue
		}
		if err := os.Remove(s.fileMetaPath(digest)); err != nil && !os.IsNotExist(err) {
			return report, err
		}
	}
	if !opts.DryRun {
		report.TmpRemoved = s.sweepTmp(now, grace)
		s.removeEmptyPrefixes()
	}
	return report, nil
}

func (s *ChunkStore) sweepTmp(now time.Time, grace time.Duration) int {
	if grace < time.Hour {
		grace = time.Hour
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "tmp"))
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < grace {
			continue
		}
		if os.Remove(filepath.Join(s.root, "tmp", entry.Name())) == nil {
			removed++
		}
	}
	return removed
}

func (s *ChunkStore) removeEmptyPrefixes() {
	root := filepath.Join(s.root, "objects")
	prefixes, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, prefix := range prefixes {
		if prefix.IsDir() {
			_ = os.Remove(filepath.Join(root, prefix.Name())) // fallisce se non vuota
		}
	}
}

// FsckReport riassume una verifica di integrità.
type FsckReport struct {
	Objects      int
	Corrupt      int
	Metas        int
	MetasBroken  int
	SnapshotsBad int
	Repaired     int
	Problems     []string
}

// Fsck rilegge ogni chunk e ne verifica lo SHA-256, poi controlla che meta e
// snapshot puntino a chunk esistenti. Con repair rimuove solo ciò che è
// ricostruibile (chunk corrotti, meta rotti): gli snapshot non si toccano.
func (s *ChunkStore) Fsck(repair bool) (FsckReport, error) {
	var report FsckReport
	problem := func(format string, args ...any) {
		if len(report.Problems) < 200 {
			report.Problems = append(report.Problems, fmt.Sprintf(format, args...))
		}
	}
	present := map[string]bool{}
	err := s.walkObjects(func(digest, path string, info os.FileInfo) error {
		report.Objects++
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		file.Close()
		if copyErr != nil {
			return copyErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != digest {
			report.Corrupt++
			problem("chunk corrotto %s", digest)
			if repair && os.Remove(path) == nil {
				report.Repaired++
			}
			return nil
		}
		present[digest] = true
		return nil
	})
	if err != nil {
		return report, err
	}
	metas, err := s.metaFiles()
	if err != nil {
		return report, err
	}
	for digest := range metas {
		report.Metas++
		chunks, loadErr := s.LoadFileMeta(digest)
		broken := loadErr != nil
		if !broken {
			for _, chunk := range chunks {
				if !present[chunk] {
					broken = true
					break
				}
			}
		}
		if !broken {
			continue
		}
		report.MetasBroken++
		problem("meta rotto %s", digest)
		if repair && os.Remove(s.fileMetaPath(digest)) == nil {
			report.Repaired++
		}
	}
	ids, err := s.ListSnapshots()
	if err != nil {
		return report, err
	}
	for _, id := range ids {
		snap, loadErr := s.LoadSnapshot(id)
		if loadErr != nil {
			report.SnapshotsBad++
			problem("snapshot %s illeggibile", id)
			continue
		}
		for _, file := range snap.Files {
			for _, chunk := range file.Chunks {
				if !present[chunk] {
					report.SnapshotsBad++
					problem("snapshot %s: chunk mancante %s (%s)", id, chunk, file.Path)
					break
				}
			}
		}
	}
	return report, nil
}

// liveDigests legge dal manifest vivo i digest dei file interi.
func (c *Context) liveDigests() (map[string]bool, error) {
	rows, err := c.renderManifest()
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(rows))
	for _, row := range rows {
		fields := strings.Split(row, "\t")
		if len(fields) > 1 && isDigest(fields[1]) {
			live[fields[1]] = true
		}
	}
	return live, nil
}

func parseChunkFlags(args []string) (dryRun, repair bool, grace time.Duration, err error) {
	grace = DefaultChunkGrace
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--dry-run":
			dryRun = true
		case "--repair":
			repair = true
		case "--grace-hours":
			index++
			if index >= len(args) {
				return false, false, 0, fmt.Errorf("--grace-hours richiede un valore")
			}
			hours, convErr := strconv.ParseFloat(args[index], 64)
			if convErr != nil || hours < 0 {
				return false, false, 0, fmt.Errorf("--grace-hours non valido: %q", args[index])
			}
			grace = time.Duration(hours * float64(time.Hour))
		default:
			return false, false, 0, fmt.Errorf("opzione sconosciuta: %q", args[index])
		}
	}
	return dryRun, repair, grace, nil
}

func cmdChunkGC(ctx *Context, args []string) error {
	dryRun, _, grace, err := parseChunkFlags(args)
	if err != nil {
		return err
	}
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	live, err := ctx.liveDigests()
	if err != nil {
		return err
	}
	maxBytes := parseConfigInt(ctx.Config["CHUNK_MAX_MB"], 0) * 1024 * 1024
	report, err := withLowPriority(func() (GCReport, error) {
		return store.GC(GCOptions{Live: live, Grace: grace, DryRun: dryRun, MaxBytes: maxBytes})
	}, int(parseConfigInt(ctx.Config["CHUNK_SCRUB_NICE"], 10)))
	if err != nil {
		return err
	}
	fmt.Println("CHUNK_GC_V1")
	fmt.Printf("DRY_RUN=%t\n", dryRun)
	fmt.Printf("OBJECTS_SCANNED=%d\nOBJECTS_REMOVED=%d\nBYTES_FREED=%d\n",
		report.ObjectsScanned, report.ObjectsRemoved, report.BytesFreed)
	fmt.Printf("METAS_SCANNED=%d\nMETAS_REMOVED=%d\nTMP_REMOVED=%d\n",
		report.MetasScanned, report.MetasRemoved, report.TmpRemoved)
	fmt.Printf("BYTES_BEFORE=%d\nBYTES_AFTER=%d\nQUOTA_BLOCKED=%t\n",
		report.BytesBefore, report.BytesAfter, report.QuotaBlocked)
	return nil
}

func cmdChunkFsck(ctx *Context, args []string) error {
	_, repair, _, err := parseChunkFlags(args)
	if err != nil {
		return err
	}
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	report, err := withLowPriority(func() (FsckReport, error) {
		return store.Fsck(repair)
	}, int(parseConfigInt(ctx.Config["CHUNK_SCRUB_NICE"], 10)))
	if err != nil {
		return err
	}
	ctx.recordFsck(report)
	fmt.Println("CHUNK_FSCK_V1")
	fmt.Printf("OBJECTS=%d\nCORRUPT=%d\nMETAS=%d\nMETAS_BROKEN=%d\nSNAPSHOT_PROBLEMS=%d\nREPAIRED=%d\n",
		report.Objects, report.Corrupt, report.Metas, report.MetasBroken, report.SnapshotsBad, report.Repaired)
	for _, line := range report.Problems {
		fmt.Println("PROBLEM=" + line)
	}
	if report.Corrupt+report.MetasBroken+report.SnapshotsBad-report.Repaired > 0 {
		return exitCode(2)
	}
	return nil
}
