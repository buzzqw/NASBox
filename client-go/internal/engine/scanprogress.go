package engine

import "sync/atomic"

// ScanProgress counts what the local scan is doing right now, so the first
// synchronisation of a big folder (100.000 files, SHA-256 of each one) shows
// real progress instead of a silent "syncing…" for minutes. Counters are
// atomics: the scan updates them from its goroutine, the UI reads them.
type ScanProgress struct {
	active  atomic.Bool
	visited atomic.Int64 // regular files looked at
	hashed  atomic.Int64 // files that had to be read to compute their digest
	bytes   atomic.Int64 // bytes read for those digests
}

// Scan is the process-wide scan progress.
var Scan = &ScanProgress{}

// ScanSnapshot is a consistent copy for display.
type ScanSnapshot struct {
	Active  bool
	Visited int64
	Hashed  int64
	Bytes   int64
}

func (s *ScanProgress) begin() {
	s.visited.Store(0)
	s.hashed.Store(0)
	s.bytes.Store(0)
	s.active.Store(true)
}

func (s *ScanProgress) end() { s.active.Store(false) }

// Snapshot returns the current counters.
func (s *ScanProgress) Snapshot() ScanSnapshot {
	return ScanSnapshot{
		Active: s.active.Load(), Visited: s.visited.Load(),
		Hashed: s.hashed.Load(), Bytes: s.bytes.Load(),
	}
}
