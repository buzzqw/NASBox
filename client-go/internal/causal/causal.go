// Package causal is the Go port of the per-path vector clock used by the
// Python client. It is what lets multiple PCs decide who wins without relying
// on wall-clock time.
package causal

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Entry struct {
	Device  string
	Counter int
}

type Version struct {
	Clock []Entry // kept sorted by device
}

func Parse(value string) (*Version, bool) {
	if strings.TrimSpace(value) == "" {
		return nil, false
	}
	version := &Version{}
	for _, component := range strings.Split(value, ",") {
		device, counterText, found := strings.Cut(component, ":")
		if !found || device == "" {
			return nil, false
		}
		counter, err := strconv.Atoi(counterText)
		if err != nil || counter < 1 {
			return nil, false
		}
		version.Clock = append(version.Clock, Entry{Device: device, Counter: counter})
	}
	if !version.valid() {
		return nil, false
	}
	version.sort()
	return version, true
}

func (v *Version) valid() bool {
	seen := map[string]bool{}
	for _, item := range v.Clock {
		if item.Device == "" || item.Counter < 1 || seen[item.Device] {
			return false
		}
		seen[item.Device] = true
	}
	return true
}

func (v *Version) sort() {
	sort.Slice(v.Clock, func(i, j int) bool { return v.Clock[i].Device < v.Clock[j].Device })
}

func (v *Version) Encode() string {
	parts := make([]string, 0, len(v.Clock))
	for _, item := range v.Clock {
		parts = append(parts, fmt.Sprintf("%s:%d", item.Device, item.Counter))
	}
	return strings.Join(parts, ",")
}

func (v *Version) Counter(device string) int {
	for _, item := range v.Clock {
		if item.Device == device {
			return item.Counter
		}
	}
	return 0
}

func (v *Version) Merge(other *Version) *Version {
	if other == nil {
		return v
	}
	values := map[string]int{}
	for _, item := range v.Clock {
		values[item.Device] = item.Counter
	}
	for _, item := range other.Clock {
		if item.Counter > values[item.Device] {
			values[item.Device] = item.Counter
		}
	}
	merged := &Version{}
	for device, counter := range values {
		merged.Clock = append(merged.Clock, Entry{Device: device, Counter: counter})
	}
	merged.sort()
	return merged
}

func (v *Version) Increment(device string) *Version {
	values := map[string]int{}
	for _, item := range v.Clock {
		values[item.Device] = item.Counter
	}
	values[device]++
	next := &Version{}
	for name, counter := range values {
		next.Clock = append(next.Clock, Entry{Device: name, Counter: counter})
	}
	next.sort()
	return next
}

// Dominates reports strict causal dominance, not a tie-break order.
func (v *Version) Dominates(other *Version) bool {
	if other == nil || len(v.Clock) == 0 {
		return false
	}
	mine := map[string]int{}
	for _, item := range v.Clock {
		mine[item.Device] = item.Counter
	}
	strict := false
	for _, item := range other.Clock {
		if mine[item.Device] < item.Counter {
			return false
		}
		if mine[item.Device] > item.Counter {
			strict = true
		}
	}
	for device, counter := range mine {
		if _, ok := other.CounterOK(device); !ok && counter > 0 {
			strict = true
		}
	}
	return strict
}

func (v *Version) CounterOK(device string) (int, bool) {
	for _, item := range v.Clock {
		if item.Device == device {
			return item.Counter, true
		}
	}
	return 0, false
}

// CompareWithDevice is the deterministic tie-break used when neither clock
// dominates: higher own counter, then device ID lexicographically.
func (v *Version) CompareWithDevice(device string, other *Version, otherDevice string) int {
	if v.Dominates(other) {
		return 1
	}
	if other.Dominates(v) {
		return -1
	}
	if v == nil {
		return -1
	}
	if other == nil {
		return 1
	}
	left, right := v.Counter(device), other.Counter(otherDevice)
	if left != right {
		if left > right {
			return 1
		}
		return -1
	}
	return strings.Compare(device, otherDevice)
}
