package engine

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Issue is one file (or folder) NASBox could not handle right now. It is not
// an error of the sync: everything else keeps syncing, and the file is retried
// by itself with a growing delay. The UI shows these as "waiting files", in
// plain language, never as a failed sync.
type Issue struct {
	Path     string
	Phase    string // read | upload | download | plan | rename
	Reason   string // technical text (the UI turns it into a sentence)
	Attempts int
	First    time.Time
	NextTry  time.Time
	seenGen  int
}

// Phases of an Issue.
const (
	PhaseRead     = "read"
	PhaseUpload   = "upload"
	PhaseDownload = "download"
	PhasePlan     = "plan"
	PhaseDelete   = "delete"
)

// issueBook is the registry of waiting files of this process.
type issueBook struct {
	mu     sync.Mutex
	gen    int
	issues map[string]*Issue
	now    func() time.Time
}

// Issues is the process-wide registry (one sync engine per process).
var Issues = &issueBook{issues: map[string]*Issue{}, now: time.Now}

// retryDelay grows 1 min, 2, 5, 15, 60: a file that keeps failing is tried
// less and less often, one that recovers is picked up within a minute.
func retryDelay(attempts int) time.Duration {
	steps := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}
	if attempts < 1 {
		attempts = 1
	}
	if attempts > len(steps) {
		attempts = len(steps)
	}
	return steps[attempts-1]
}

// BeginPass starts a sync pass: read problems must be reported again by this
// pass to stay (a file that became readable disappears by itself).
func (b *issueBook) BeginPass() {
	b.mu.Lock()
	b.gen++
	b.mu.Unlock()
}

// EndPass forgets read problems that this pass did not see again.
func (b *issueBook) EndPass() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for path, issue := range b.issues {
		if issue.Phase == PhaseRead && issue.seenGen < b.gen {
			delete(b.issues, path)
		}
	}
}

// Report records (or updates) a problem and schedules the next attempt.
func (b *issueBook) Report(path, phase, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	issue, ok := b.issues[path]
	if !ok {
		issue = &Issue{Path: path, First: now}
		b.issues[path] = issue
	}
	issue.Phase, issue.Reason, issue.seenGen = phase, reason, b.gen
	issue.Attempts++
	issue.NextTry = now.Add(retryDelay(issue.Attempts))
}

// Clear forgets a problem (the file went through).
func (b *issueBook) Clear(path string) {
	b.mu.Lock()
	delete(b.issues, path)
	b.mu.Unlock()
}

// Waiting reports whether the path is inside its retry delay and must be left
// out of this pass.
func (b *issueBook) Waiting(path string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	issue, ok := b.issues[path]
	return ok && issue.Phase != PhaseRead && b.now().Before(issue.NextTry)
}

// Held reports whether the path (or a folder above it) could not be read by
// this pass. Such a path is neither uploaded nor treated as deleted: the file
// is still there, NASBox just cannot see it right now.
func (b *issueBook) Held(path string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for held, issue := range b.issues {
		if issue.Phase != PhaseRead || issue.seenGen < b.gen {
			continue
		}
		if path == held || strings.HasPrefix(path, held+"/") {
			return true
		}
	}
	return false
}

// Snapshot returns the waiting files, oldest problem first.
func (b *issueBook) Snapshot() []Issue {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Issue, 0, len(b.issues))
	for _, issue := range b.issues {
		out = append(out, *issue)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].First.Equal(out[j].First) {
			return out[i].First.Before(out[j].First)
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Count is the number of waiting files.
func (b *issueBook) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.issues)
}

// globalFailure tells a failure that concerns the whole transfer (network,
// lock held by another PC, disk full, host key) from one that concerns a
// single file. Global failures abort the pass and are retried as before;
// single-file failures are isolated.
// fatalError marks a failure of NASBox's own state (database), which is never
// about one file and must stop the pass.
type fatalError struct{ err error }

func (f fatalError) Error() string { return f.err.Error() }
func (f fatalError) Unwrap() error { return f.err }

func unwrapFatal(err error) error {
	var fatal fatalError
	if errors.As(err, &fatal) {
		return fatal.err
	}
	return err
}

func globalFailure(err error) bool {
	if err == nil {
		return false
	}
	var fatal fatalError
	if errors.As(err, &fatal) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, token := range []string{
		"exit status 255", "connection", "timed out", "timeout", "no route", "could not resolve",
		"broken pipe", "host key", "permission denied (publickey", "flock", "lease", "nas occupato",
		"lock non", "no space left", "disk quota", "journal nas", "context canceled",
		"context deadline", "server non", "input/output error",
	} {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

// isolateBatch runs `run` on paths and, when it fails for a reason that is not
// global, splits the batch in halves until the failing files are found: those
// are reported as waiting files and everything else goes through. At most
// `budget` runs are made per call so a pass cannot spend its time bisecting.
func isolateBatch(paths []string, phase string, budget int, run func([]string) error) error {
	remaining := budget
	var recurse func(batch []string) error
	recurse = func(batch []string) error {
		if len(batch) == 0 {
			return nil
		}
		if remaining <= 0 {
			return nil // the rest is simply replanned by the next pass
		}
		remaining--
		err := run(batch)
		if err == nil {
			for _, path := range batch {
				Issues.Clear(path)
			}
			return nil
		}
		if globalFailure(err) {
			return err
		}
		if len(batch) == 1 {
			Issues.Report(batch[0], phase, err.Error())
			return nil
		}
		middle := len(batch) / 2
		if err := recurse(batch[:middle]); err != nil {
			return err
		}
		return recurse(batch[middle:])
	}
	return recurse(paths)
}

// RetryNow makes every waiting file eligible at the next pass.
func (b *issueBook) RetryNow() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, issue := range b.issues {
		issue.NextTry = b.now()
	}
}

// PendingDeletes are the deletions a safety guard is holding back: files the
// user removed here (or that disappeared) which NASBox will not remove from
// the NAS without a decision. The UI offers "delete them too" or "bring them
// back".
type pendingBook struct {
	mu    sync.Mutex
	paths []string
}

// PendingDeletes is the process-wide list of held deletions.
var PendingDeletes = &pendingBook{}

// Set replaces the list (nil clears it).
func (p *pendingBook) Set(paths []string) {
	p.mu.Lock()
	p.paths = append([]string(nil), paths...)
	p.mu.Unlock()
}

// Paths returns a copy of the held paths.
func (p *pendingBook) Paths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.paths...)
}

// FolderCount is a folder with how many held files it contains.
type FolderCount struct {
	Folder string
	Count  int
}

// Folders groups the held files by their first two path components, biggest
// first, so "2.034 files in SADE/SADE" is readable at a glance.
func (p *pendingBook) Folders(limit int) []FolderCount {
	counts := map[string]int{}
	for _, path := range p.Paths() {
		parts := strings.Split(path, "/")
		key := parts[0]
		if len(parts) > 2 {
			key = parts[0] + "/" + parts[1]
		}
		counts[key]++
	}
	out := make([]FolderCount, 0, len(counts))
	for folder, count := range counts {
		out = append(out, FolderCount{Folder: folder, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Folder < out[j].Folder
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
