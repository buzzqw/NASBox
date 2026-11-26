package serverd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

// phaseStat accumulates server-side timings. The old bash daemon only logged
// them; a CLI verb is a separate process, so counters must live on disk to be
// visible to `nasbox-server --metrics`.
type phaseStat struct {
	Calls        int64   `json:"calls"`
	SecondsTotal float64 `json:"seconds_total"`
	SecondsMax   float64 `json:"seconds_max"`
	SecondsLast  float64 `json:"seconds_last"`
}

type dedupCounters struct {
	Queries    int64 `json:"queries"`
	Digests    int64 `json:"digests"`
	Hits       int64 `json:"hits"`
	Misses     int64 `json:"misses"`
	BytesSaved int64 `json:"bytes_saved"`
	PutObjects int64 `json:"put_objects"`
	PutStored  int64 `json:"put_stored"`
}

type manifestCounters struct {
	DeltaRequests   int64 `json:"delta_requests"`
	FullFallbacks   int64 `json:"full_fallbacks"`
	DeltaRows       int64 `json:"delta_rows"`
	DeltaBytes      int64 `json:"delta_bytes"`
	DeltaBytesSaved int64 `json:"delta_bytes_saved"`
	FullBytes       int64 `json:"full_bytes"`
	ReusedRenders   int64 `json:"reused_renders"`
}

type leaseStat struct {
	Grants           int64 `json:"grants"`
	WaitSecondsTotal int64 `json:"wait_seconds_total"`
	WaitSecondsMax   int64 `json:"wait_seconds_max"`
	WaitSecondsLast  int64 `json:"wait_seconds_last"`
	HoldSecondsTotal int64 `json:"hold_seconds_total"`
	HoldSecondsMax   int64 `json:"hold_seconds_max"`
}

type fsckLast struct {
	TS               int64 `json:"ts"`
	Objects          int   `json:"objects"`
	Corrupt          int   `json:"corrupt"`
	Metas            int   `json:"metas"`
	MetasBroken      int   `json:"metas_broken"`
	SnapshotProblems int   `json:"snapshot_problems"`
	Repaired         int   `json:"repaired"`
	Runs             int64 `json:"runs"`
}

type serverCounters struct {
	Started  int64                 `json:"started"`
	Phases   map[string]*phaseStat `json:"phases"`
	Dedup    dedupCounters         `json:"dedup"`
	Manifest manifestCounters      `json:"manifest"`
	Lease    map[string]*leaseStat `json:"lease"`
	Fsck     fsckLast              `json:"fsck"`
}

var counterStores sync.Map // StateDir -> *serverCounters

// serverCounters returns the per-state-directory counter cache. Each process
// reloads the file on every mutation: verbs are short-lived SSH processes and
// cannot rely on in-memory state.
func (c *Context) serverCounters() *serverCounters {
	if c == nil || c.StateDir == "" {
		return &serverCounters{Phases: map[string]*phaseStat{}, Lease: map[string]*leaseStat{}}
	}
	if cached, ok := counterStores.Load(c.StateDir); ok {
		return cached.(*serverCounters)
	}
	store := &serverCounters{}
	if raw, err := os.ReadFile(c.counterPath()); err == nil {
		_ = json.Unmarshal(raw, store)
	}
	store.ensure()
	actual, _ := counterStores.LoadOrStore(c.StateDir, store)
	return actual.(*serverCounters)
}

func (c *Context) counterPath() string { return filepath.Join(c.StateDir, "metrics-counters.json") }

func (s *serverCounters) ensure() {
	if s.Started == 0 {
		s.Started = time.Now().Unix()
	}
	if s.Phases == nil {
		s.Phases = map[string]*phaseStat{}
	}
	if s.Lease == nil {
		s.Lease = map[string]*leaseStat{}
	}
}

func (c *Context) updateCounters(fn func(*serverCounters)) {
	store := c.serverCounters()
	if err := c.withCounterLock(func() error {
		// Reload to include writes made by another short-lived verb.
		if raw, err := os.ReadFile(c.counterPath()); err == nil {
			fresh := &serverCounters{}
			if json.Unmarshal(raw, fresh) == nil {
				fresh.ensure()
				*store = *fresh
			}
		}
		fn(store)
		return c.saveCounters(store)
	}); err != nil {
		// Counters are diagnostics only: never fail the actual operation.
		fmt.Fprintln(os.Stderr, "contatori:", err)
	}
}

func (c *Context) withCounterLock(fn func() error) error {
	lockPath := c.counterPath() + ".lock"
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return fn()
}

func (c *Context) saveCounters(store *serverCounters) error {
	content, err := json.Marshal(store)
	if err != nil {
		return err
	}
	tmp := c.counterPath() + ".tmp." + fmt.Sprint(os.Getpid())
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.counterPath())
}

// recordPhase stores a duration for one server phase.
func (c *Context) recordPhase(name string, d time.Duration) {
	seconds := d.Seconds()
	if seconds < 0 {
		return
	}
	c.updateCounters(func(s *serverCounters) {
		p := s.Phases[name]
		if p == nil {
			p = &phaseStat{}
			s.Phases[name] = p
		}
		p.Calls++
		p.SecondsTotal += seconds
		p.SecondsLast = seconds
		if seconds > p.SecondsMax {
			p.SecondsMax = seconds
		}
	})
}

func (c *Context) recordChunkHas(digests, hits int64) {
	c.updateCounters(func(s *serverCounters) {
		s.Dedup.Queries++
		s.Dedup.Digests += digests
		s.Dedup.Hits += hits
		s.Dedup.Misses += digests - hits
		s.Dedup.BytesSaved += hits * int64(c.ChunkSize())
	})
}

func (c *Context) recordChunkPut(stored int64) {
	c.updateCounters(func(s *serverCounters) {
		s.Dedup.PutObjects++
		s.Dedup.PutStored += stored
	})
}

func (c *Context) recordRenderCacheHit() {
	c.updateCounters(func(s *serverCounters) { s.Manifest.ReusedRenders++ })
}

func (c *Context) recordManifestExport(delta, fallback bool, rows int, bytes int64) {
	c.updateCounters(func(s *serverCounters) {
		if !delta {
			s.Manifest.FullBytes = bytes
			return
		}
		s.Manifest.DeltaRequests++
		s.Manifest.DeltaRows += int64(rows)
		s.Manifest.DeltaBytes += bytes
		if fallback {
			s.Manifest.FullFallbacks++
			s.Manifest.FullBytes = bytes
		} else {
			s.Manifest.DeltaBytesSaved += s.Manifest.FullBytes - bytes
			if s.Manifest.DeltaBytesSaved < 0 {
				s.Manifest.DeltaBytesSaved = 0
			}
		}
	})
}

func (c *Context) recordLease(device string, wait, hold time.Duration) {
	if device == "" {
		return
	}
	waitSeconds, holdSeconds := int64(wait.Seconds()), int64(hold.Seconds())
	c.updateCounters(func(s *serverCounters) {
		stat := s.Lease[device]
		if stat == nil {
			stat = &leaseStat{}
			s.Lease[device] = stat
		}
		stat.Grants++
		stat.WaitSecondsTotal += waitSeconds
		stat.WaitSecondsLast = waitSeconds
		if waitSeconds > stat.WaitSecondsMax {
			stat.WaitSecondsMax = waitSeconds
		}
		stat.HoldSecondsTotal += holdSeconds
		if holdSeconds > stat.HoldSecondsMax {
			stat.HoldSecondsMax = holdSeconds
		}
	})
}

func (c *Context) recordFsck(report FsckReport) {
	c.updateCounters(func(s *serverCounters) {
		s.Fsck = fsckLast{
			TS: time.Now().Unix(), Objects: report.Objects, Corrupt: report.Corrupt,
			Metas: report.Metas, MetasBroken: report.MetasBroken,
			SnapshotProblems: report.SnapshotsBad, Repaired: report.Repaired,
			Runs: s.Fsck.Runs + 1,
		}
	})
}

// countersLines renders the diagnostic keys appended to --metrics.
func (c *Context) countersLines() []string {
	store := c.serverCounters()
	store.ensure()
	lines := []string{
		fmt.Sprintf("SERVER_COUNTERS_STARTED_AT=%d", store.Started),
		fmt.Sprintf("PERF_PHASES=%s", sortedPhaseNames(store.Phases)),
	}
	for _, name := range sortedPhaseNames(store.Phases) {
		p := store.Phases[name]
		prefix := "PERF_" + name + "_"
		lines = append(lines,
			fmt.Sprintf("%sCALLS=%d", prefix, p.Calls),
			fmt.Sprintf("%sSECONDS_TOTAL=%.3f", prefix, p.SecondsTotal),
			fmt.Sprintf("%sSECONDS_MAX=%.3f", prefix, p.SecondsMax),
			fmt.Sprintf("%sSECONDS_LAST=%.3f", prefix, p.SecondsLast),
		)
	}
	d := store.Dedup
	hitRate := 0.0
	if d.Digests > 0 {
		hitRate = float64(d.Hits) * 100 / float64(d.Digests)
	}
	lines = append(lines,
		fmt.Sprintf("CHUNK_HAS_QUERIES=%d", d.Queries),
		fmt.Sprintf("CHUNK_HAS_DIGESTS=%d", d.Digests),
		fmt.Sprintf("CHUNK_HAS_HITS=%d", d.Hits),
		fmt.Sprintf("CHUNK_HAS_MISSES=%d", d.Misses),
		fmt.Sprintf("CHUNK_HAS_HIT_RATE_PERCENT=%.1f", hitRate),
		fmt.Sprintf("CHUNK_HAS_BYTES_SAVED=%d", d.BytesSaved),
		fmt.Sprintf("CHUNK_PUT_OBJECTS=%d", d.PutObjects),
		fmt.Sprintf("CHUNK_PUT_STORED=%d", d.PutStored),
	)
	m := store.Manifest
	lines = append(lines,
		fmt.Sprintf("MANIFEST_DELTA_REQUESTS=%d", m.DeltaRequests),
		fmt.Sprintf("MANIFEST_DELTA_FULL_FALLBACKS=%d", m.FullFallbacks),
		fmt.Sprintf("MANIFEST_DELTA_ROWS=%d", m.DeltaRows),
		fmt.Sprintf("MANIFEST_DELTA_BYTES=%d", m.DeltaBytes),
		fmt.Sprintf("MANIFEST_FULL_BYTES=%d", m.FullBytes),
		fmt.Sprintf("MANIFEST_DELTA_BYTES_SAVED=%d", m.DeltaBytesSaved),
		fmt.Sprintf("MANIFEST_RENDER_CACHE_HITS=%d", m.ReusedRenders),
	)
	var totalWait, totalHold, maxWait int64
	lockDeviceStats := "["
	first := true
	for name, stat := range store.Lease {
		totalWait += stat.WaitSecondsTotal
		totalHold += stat.HoldSecondsTotal
		if stat.WaitSecondsMax > maxWait {
			maxWait = stat.WaitSecondsMax
		}
		if !first {
			lockDeviceStats += ","
		}
		first = false
		lockDeviceStats += fmt.Sprintf(`{"device":%q,"grants":%d,"wait_s":%d,"hold_s":%d}`,
			name, stat.Grants, stat.WaitSecondsTotal, stat.HoldSecondsTotal)
	}
	lockDeviceStats += "]"
	lines = append(lines,
		fmt.Sprintf("LOCK_WAIT_SECONDS_TOTAL=%d", totalWait),
		fmt.Sprintf("LOCK_WAIT_SECONDS_MAX=%d", maxWait),
		fmt.Sprintf("LOCK_HOLD_SECONDS_TOTAL=%d", totalHold),
		fmt.Sprintf("LOCK_DEVICE_STATS_JSON=%s", lockDeviceStats),
	)
	f := store.Fsck
	ok := 1
	if f.Corrupt > 0 || f.MetasBroken > 0 || f.SnapshotProblems > 0 {
		ok = 0
	}
	lines = append(lines,
		fmt.Sprintf("CHUNK_FSCK_RUNS=%d", f.Runs),
		fmt.Sprintf("CHUNK_FSCK_LAST_TS=%d", f.TS),
		fmt.Sprintf("CHUNK_FSCK_LAST_OBJECTS=%d", f.Objects),
		fmt.Sprintf("CHUNK_FSCK_LAST_CORRUPT=%d", f.Corrupt),
		fmt.Sprintf("CHUNK_FSCK_LAST_METAS_BROKEN=%d", f.MetasBroken),
		fmt.Sprintf("CHUNK_FSCK_LAST_SNAPSHOT_PROBLEMS=%d", f.SnapshotProblems),
		fmt.Sprintf("CHUNK_FSCK_LAST_REPAIRED=%d", f.Repaired),
		fmt.Sprintf("CHUNK_FSCK_LAST_OK=%d", ok),
	)
	return lines
}

func sortedPhaseNames(phases map[string]*phaseStat) []string {
	names := make([]string, 0, len(phases))
	for name := range phases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
