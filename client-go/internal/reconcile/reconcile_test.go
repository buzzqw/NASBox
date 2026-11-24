package reconcile

import (
	"strings"
	"testing"

	"nasbox-go/internal/causal"
)

// These cases are a direct port of client/tests/test_reconcile.py. Both
// clients must pick the same convergent action from the same common baseline.

func fp(digest string, mtimeNS int64, causalText string) *Fingerprint {
	return &Fingerprint{
		Digest: digest, Size: int64(len(digest)),
		MTimeNS: mtimeNS, Causal: causalText,
	}
}

func remoteState(kind Kind, digest string, mtimeNS int64, causalText string) RemoteState {
	return RemoteState{
		Kind: kind, Digest: digest, Size: int64(len(digest)),
		MTimeNS: mtimeNS, Causal: causalText,
	}
}

func clock(entries ...causal.Entry) string {
	return (&causal.Version{Clock: entries}).Encode()
}

func TestPlanMatchesPython(t *testing.T) {
	const (
		base10 = int64(10_000_000_000)
		base20 = int64(20_000_000_000)
	)
	clockA1 := clock(causal.Entry{Device: "device-a", Counter: 1})
	clockA2 := clock(causal.Entry{Device: "device-a", Counter: 2})
	clockA3B2 := clock(
		causal.Entry{Device: "device-a", Counter: 3},
		causal.Entry{Device: "device-b", Counter: 2},
	)
	clockAB1 := clock(
		causal.Entry{Device: "device-a", Counter: 1},
		causal.Entry{Device: "device-b", Counter: 1},
	)
	clockA2B1 := clock(
		causal.Entry{Device: "device-a", Counter: 2},
		causal.Entry{Device: "device-b", Counter: 1},
	)

	cases := []struct {
		name        string
		baseline    *Fingerprint
		local       *Fingerprint
		remote      RemoteState
		localCausal string
		delete      bool
		policy      string
		want        Action
		detail      string
	}{
		{"unknown history adopts equal remote file", nil, fp("same", base10, ""),
			remoteState(File, "same", base10, ""), "", true, "manual", Adopt, ""},
		{"unknown history uploads when remote absent", nil, fp("local", base10, ""),
			remoteState(Absent, "", 0, ""), "", true, "manual", Upload, ""},
		{"unknown history uses timestamp", nil, fp("local", 2*base10, ""),
			remoteState(File, "remote", base10, ""), "", true, "manual", ConflictLocalWins, ""},
		{"unknown history keeps remote on equal second", nil, fp("local", base10, ""),
			remoteState(File, "remote", base10, ""), "", true, "manual", ConflictRemoteWins, ""},
		{"baseline local change uploads", fp("base", base10, ""), fp("local", base10, ""),
			remoteState(File, "base", base10, ""), "", true, "manual", Upload, ""},
		{"baseline remote change wins", fp("base", base10, ""), fp("base", base10, ""),
			remoteState(File, "remote", base10, ""), "", true, "manual", RemoteWins, ""},
		{"concurrent change uses remote on equal second", fp("base", base10, ""), fp("local", 2*base10, ""),
			remoteState(File, "remote", 2*base10, ""), "", true, "manual", ConflictRemoteWins, ""},
		{"local delete only deletes matching baseline", fp("base", base10, ""), nil,
			remoteState(File, "base", base10, ""), "", true, "manual", DeleteRemote, ""},
		{"stale local delete does not delete changed remote", fp("base", base10, ""), nil,
			remoteState(File, "new", base10, ""), "", true, "manual", RemoteWins, ""},
		{"delete disabled never deletes", fp("base", base10, ""), nil,
			remoteState(File, "base", base10, ""), "", false, "manual", RemoteWins, ""},
		{"remote deletion does not erase concurrent local change", fp("base", base10, ""), fp("local", base10, ""),
			remoteState(Tombstone, "", base10, ""), "", true, "manual", ConflictRemoteWins, ""},
		{"local edit newer than remote deletion wins", fp("base", 9_000_000_000, ""), fp("local", 3*base10, ""),
			remoteState(Tombstone, "", base10, ""), "", false, "manual", ConflictLocalWins, "piu' recente"},
		{"local edit older than remote deletion loses", fp("base", 9_000_000_000, ""), fp("local", base10, ""),
			remoteState(Tombstone, "", 2*base10, ""), "", true, "manual", ConflictRemoteWins, ""},
		{"absent remote never beats local file", fp("base", base10, ""), fp("local", base10, ""),
			remoteState(Absent, "", 0, ""), "", true, "manual", ConflictLocalWins, ""},
		{"absent remote with unchanged local is adopted", fp("base", base10, ""), fp("base", base10, ""),
			remoteState(Absent, "", 0, ""), "", true, "manual", RemoteWins, ""},
		{"known tombstone allows local recreation", fp("", 0, ""), fp("recreated", base10, ""),
			remoteState(Tombstone, "", base10, ""), "", true, "manual", Upload, ""},
		{"unmanaged remote directory ignored", nil, nil,
			remoteState(Other, "", 0, ""), "", true, "manual", Nothing, ""},
		{"expected remote directory blocks", fp("base", base10, ""), nil,
			remoteState(Other, "", 0, ""), "", true, "manual", Block, ""},
		{"identical states adopted", fp("base", base10, ""), fp("base", base10, ""),
			remoteState(File, "base", base10, ""), "", true, "manual", Adopt, ""},
		{"causal local version wins before timestamp", fp("base", 10, clockA1), fp("local", 10, clockAB1),
			remoteState(File, "remote", 20, clockA1), "", true, "manual", ConflictLocalWins, "causalmente"},
		{"causal remote tombstone wins before timestamp", fp("base", 10, clockA1), fp("local", 20, clockA2),
			remoteState(Tombstone, "", 10, clockA2B1), "", true, "manual", ConflictRemoteWins, "causalmente"},
		{"newest uses subsecond only when both are precise", nil, fp("local", 20_900_000_000, ""),
			remoteState(File, "remote", 20_100_000_000, ""), "", true, "newest", ConflictLocalWins, ""},
		{"newest ignores one-sided subsecond precision", nil, fp("local", 20_900_000_000, ""),
			remoteState(File, "remote", 21_000_000_000, ""), "", true, "newest", ConflictRemoteWins, ""},
		{"newest breaks exact ties by digest", nil, fp("bbb", 2*base10, ""),
			remoteState(File, "aaa", 2*base10, ""), "", true, "newest", ConflictLocalWins, ""},
		{"newest breaks reversed ties by digest", nil, fp("aaa", 2*base10, ""),
			remoteState(File, "bbb", 2*base10, ""), "", true, "newest", ConflictRemoteWins, ""},
		{"manual keeps remote on exact tie", nil, fp("bbb", 2*base10, ""),
			remoteState(File, "aaa", 2*base10, ""), "", true, "manual", ConflictRemoteWins, ""},
		{"concurrent vectors fall back to timestamp", fp("base", base10, clockA1),
			fp("local", 2*base10, clockA2),
			remoteState(File, "remote", base10, clockAB1), "", true, "manual", ConflictLocalWins, ""},
		{"observed local deletion causal is used when local is absent", fp("base", base10, clockA1), nil,
			remoteState(File, "remote", 2*base10, clockA2B1), clockA3B2, true, "manual", DeleteRemote, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Plan(tc.baseline, tc.local, tc.remote, tc.localCausal, tc.delete, tc.policy)
			if got.Action != tc.want {
				t.Fatalf("action = %s (%s), want %s", got.Action, got.Detail, tc.want)
			}
			if tc.detail != "" && !strings.Contains(got.Detail, tc.detail) {
				t.Fatalf("detail = %q, want it to contain %q", got.Detail, tc.detail)
			}
		})
	}
}
