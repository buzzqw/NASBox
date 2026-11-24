package conflicts

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	DefaultConflictStormLimit         = 3
	DefaultConflictStormWindowSeconds = 3600
	maxTrackedConflictStormPaths      = 4096
	maxTrackedConflictStormVersions   = 16384
)

// StormConfig uses the same setting names as the Python client.
// ConflictStormLimit is the maximum number of distinct losing versions retained
// per original path and origin device during ConflictStormWindowSeconds.
type StormConfig struct {
	ConflictStormLimit         int
	ConflictStormWindowSeconds int
}

// ConflictPlan identifies the copy that a conflict event should update.
// Reused is true for a retry of the same origin/digest or for a storm slot.
// Storm is true only when the configured limit redirects a new digest to an
// already retained copy.
type ConflictPlan struct {
	Path   string
	Reused bool
	Storm  bool
}

type stormKey struct {
	root     string
	original string
	origin   string
}

type stormVersion struct {
	digest   string
	path     string
	lastSeen time.Time
}

type stormBucket struct {
	versions map[string]*stormVersion
	order    []*stormVersion
}

// StormTracker bounds conflict-copy growth while making retries deterministic.
// It is safe for concurrent use. A tracker should live as long as the sync
// worker so its window survives individual sync passes.
type StormTracker struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	now     func() time.Time
	buckets map[stormKey]*stormBucket
}

// NewStormTracker creates a tracker with Python-compatible defaults for zero or
// invalid configuration values.
func NewStormTracker(config StormConfig) *StormTracker {
	limit := config.ConflictStormLimit
	if limit <= 0 {
		limit = DefaultConflictStormLimit
	}
	windowSeconds := config.ConflictStormWindowSeconds
	if windowSeconds <= 0 {
		windowSeconds = DefaultConflictStormWindowSeconds
	}
	return &StormTracker{
		limit:   limit,
		window:  time.Duration(windowSeconds) * time.Second,
		now:     time.Now,
		buckets: make(map[stormKey]*stormBucket),
	}
}

var defaultStormTracker = NewStormTracker(StormConfig{})

// Plan selects a deterministic destination for one conflict event. The limit
// is applied per root, original path and origin device: copies from different
// devices never overwrite each other or lose their origin metadata.
func (tracker *StormTracker) Plan(root, originalRelative, originDevice, digest string) (ConflictPlan, error) {
	relative, err := cleanRelative(originalRelative)
	if err != nil {
		return ConflictPlan{}, err
	}
	origin := sanitizeOrigin(originDevice)
	key := stormKey{root: rootIdentity(root), original: relative, origin: origin}

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	now := tracker.now()
	tracker.prune(now)

	bucket := tracker.buckets[key]
	if bucket == nil {
		bucket = &stormBucket{versions: make(map[string]*stormVersion)}
		tracker.buckets[key] = bucket
	}
	if existing := bucket.versions[digest]; existing != nil {
		existing.lastSeen = now
		return ConflictPlan{Path: existing.path, Reused: true}, nil
	}

	plan := ConflictPlan{}
	if len(bucket.order) < tracker.limit {
		plan.Path, err = conflictRelativeName(relative, origin, digest)
		if err != nil {
			return ConflictPlan{}, err
		}
	} else {
		victim := oldestStormVersion(bucket.order)
		plan.Path = victim.path
		plan.Reused = true
		plan.Storm = true
	}
	version := &stormVersion{digest: digest, path: plan.Path, lastSeen: now}
	bucket.versions[digest] = version
	bucket.order = append(bucket.order, version)
	tracker.boundVersions()
	return plan, nil
}

// Create preserves the local version under the path selected by Plan. The
// write is staged in the destination directory and atomically renamed into
// place, so retries never expose a partial conflict copy.
func (tracker *StormTracker) Create(root, originalRelative, originDevice, digest string) (string, error) {
	plan, err := tracker.Plan(root, originalRelative, originDevice, digest)
	if err != nil {
		return "", err
	}
	return createCopy(root, originalRelative, plan.Path)
}

// Create uses the default Python-compatible storm settings. Callers that need
// configurable limits should keep a StormTracker and use its Create method.
func Create(root, originalRelative, originDevice, digest string) (string, error) {
	return defaultStormTracker.Create(root, originalRelative, originDevice, digest)
}

func oldestStormVersion(versions []*stormVersion) *stormVersion {
	oldest := versions[0]
	for _, version := range versions[1:] {
		if version.lastSeen.Before(oldest.lastSeen) {
			oldest = version
		}
	}
	return oldest
}

func (tracker *StormTracker) prune(now time.Time) {
	cutoff := now.Add(-tracker.window)
	for key, bucket := range tracker.buckets {
		kept := bucket.order[:0]
		for _, version := range bucket.order {
			if version.lastSeen.Before(cutoff) {
				delete(bucket.versions, version.digest)
				continue
			}
			kept = append(kept, version)
		}
		bucket.order = kept
		if len(kept) == 0 {
			delete(tracker.buckets, key)
		}
	}
	if len(tracker.buckets) <= maxTrackedConflictStormPaths {
		return
	}
	keys := make([]stormKey, 0, len(tracker.buckets))
	for key := range tracker.buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := tracker.buckets[keys[i]], tracker.buckets[keys[j]]
		leftTime, rightTime := oldestStormVersion(left.order).lastSeen, oldestStormVersion(right.order).lastSeen
		if leftTime.Equal(rightTime) {
			return fmt.Sprintf("%s\x00%s\x00%s", keys[i].root, keys[i].original, keys[i].origin) <
				fmt.Sprintf("%s\x00%s\x00%s", keys[j].root, keys[j].original, keys[j].origin)
		}
		return leftTime.Before(rightTime)
	})
	for _, key := range keys[:len(keys)-maxTrackedConflictStormPaths] {
		delete(tracker.buckets, key)
	}
}

func (tracker *StormTracker) boundVersions() {
	total := 0
	for _, bucket := range tracker.buckets {
		total += len(bucket.order)
	}
	for total > maxTrackedConflictStormVersions {
		var oldestKey stormKey
		var oldest *stormVersion
		for key, bucket := range tracker.buckets {
			candidate := oldestStormVersion(bucket.order)
			if oldest == nil || candidate.lastSeen.Before(oldest.lastSeen) ||
				(candidate.lastSeen.Equal(oldest.lastSeen) && candidate.path < oldest.path) {
				oldestKey, oldest = key, candidate
			}
		}
		if oldest == nil {
			return
		}
		bucket := tracker.buckets[oldestKey]
		delete(bucket.versions, oldest.digest)
		kept := bucket.order[:0]
		for _, version := range bucket.order {
			if version != oldest {
				kept = append(kept, version)
			}
		}
		bucket.order = kept
		if len(kept) == 0 {
			delete(tracker.buckets, oldestKey)
		}
		total--
	}
}

func rootIdentity(root string) string {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return filepath.Clean(root)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	return filepath.Clean(absolute)
}
