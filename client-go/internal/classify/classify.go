// Package classify is the Go port of the client-side difference classifier.
// It runs entirely on the PC: the NAS never classifies anything.
package classify

import (
	"fmt"
	"sort"
	"strings"
)

type RemoteState struct {
	Kind    string // "FILE" or "TOMBSTONE"
	Digest  string
	Size    int64
	MTimeNS int64
}

type Identity struct {
	Digest  string
	Size    int64
	CtimeNS int64
}

type Rename struct {
	Missing string
	Local   string
	Reason  string
}

type Anomaly struct {
	Dir     string
	Missing int
	Remote  int
}

func (a Anomaly) Ratio() float64 {
	if a.Remote == 0 {
		return 1.0
	}
	return float64(a.Missing) / float64(a.Remote)
}

type Report struct {
	MissingTotal int
	Renames      []Rename
	Unexplained  []string
	MissingByDir map[string]int
	RemoteByDir  map[string]int
	Anomalies    []Anomaly
}

// Summary is a compact, human-readable description of the report.
func (r Report) Summary() string {
	parts := make([]string, 0, 3)
	if len(r.Renames) > 0 {
		parts = append(parts, fmt.Sprintf("%d rename", len(r.Renames)))
	}
	if len(r.Unexplained) > 0 {
		parts = append(parts, fmt.Sprintf("%d cancellazioni sospette", len(r.Unexplained)))
	}
	if len(r.Anomalies) > 0 {
		shown := r.Anomalies
		if len(shown) > 4 {
			shown = shown[:4]
		}
		details := make([]string, 0, len(shown))
		for _, item := range shown {
			details = append(details, fmt.Sprintf("%s %d/%d (%.0f%%)",
				item.Dir, item.Missing, item.Remote, item.Ratio()*100))
		}
		parts = append(parts, "cartelle anomale: "+strings.Join(details, ", "))
	}
	if len(parts) == 0 {
		return "nessuna differenza rilevante"
	}
	return strings.Join(parts, "; ")
}

func topDir(path string) string {
	for i, c := range path {
		if c == '/' {
			return path[:i]
		}
	}
	return path
}

// Classify separates renames from genuine deletions and flags directories
// that lost an improbable share of their remote files.
func Classify(
	missing []string,
	remote map[string]RemoteState,
	identity map[string]Identity,
	dirRatio float64,
	minFiles int,
) Report {
	byDigest := make(map[string][]string)
	for path, info := range identity {
		if info.Digest != "" {
			byDigest[info.Digest] = append(byDigest[info.Digest], path)
		}
	}
	report := Report{
		MissingTotal: len(missing),
		MissingByDir: make(map[string]int),
		RemoteByDir:  make(map[string]int),
	}
	consumed := make(map[string]bool)
	for _, path := range missing {
		state := remote[path]
		var match *Rename
		for _, localPath := range byDigest[state.Digest] {
			if consumed[localPath] || localPath == path {
				continue
			}
			info := identity[localPath]
			if info.Size != state.Size && state.Size != 0 {
				continue
			}
			reason := "digest"
			if info.CtimeNS != 0 {
				reason = "digest+mtime"
			}
			match = &Rename{Missing: path, Local: localPath, Reason: reason}
			consumed[localPath] = true
			break
		}
		if match != nil {
			report.Renames = append(report.Renames, *match)
		} else {
			report.Unexplained = append(report.Unexplained, path)
		}
	}
	// Anomalies count only unexplained losses: a folder rename produces many
	// missing paths that are all matched to new local names.
	for _, path := range report.Unexplained {
		report.MissingByDir[topDir(path)]++
	}
	for path, state := range remote {
		if state.Kind == "FILE" {
			report.RemoteByDir[topDir(path)]++
		}
	}
	for dir, count := range report.MissingByDir {
		total := report.RemoteByDir[dir]
		if count >= minFiles && total > 0 && float64(count)/float64(total) >= dirRatio {
			report.Anomalies = append(report.Anomalies, Anomaly{Dir: dir, Missing: count, Remote: total})
		}
	}
	sort.Slice(report.Anomalies, func(i, j int) bool {
		if report.Anomalies[i].Missing != report.Anomalies[j].Missing {
			return report.Anomalies[i].Missing > report.Anomalies[j].Missing
		}
		return report.Anomalies[i].Dir < report.Anomalies[j].Dir
	})
	return report
}
