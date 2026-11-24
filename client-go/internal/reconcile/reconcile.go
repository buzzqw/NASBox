// Package reconcile contains the pure per-path decision rules. It mirrors the
// mature Python implementation so both clients choose the same convergent
// action from the common baseline and the two live states.
package reconcile

import (
	"strings"

	"nasbox-go/internal/causal"
)

type Kind string

const (
	Absent    Kind = "ABSENT"
	File      Kind = "FILE"
	Tombstone Kind = "TOMBSTONE"
	Other     Kind = "OTHER"
)

type Fingerprint struct {
	Digest  string
	Size    int64
	MTimeNS int64
	Causal  string
}

func (f *Fingerprint) IsTombstone() bool { return f == nil || f.Digest == "" }

type RemoteState struct {
	Kind    Kind
	Digest  string
	Size    int64
	MTimeNS int64
	Causal  string
}

type Action string

const (
	Nothing            Action = "nothing"
	Adopt              Action = "adopt"
	Upload             Action = "upload"
	DeleteRemote       Action = "delete_remote"
	RemoteWins         Action = "remote_wins"
	ConflictLocalWins  Action = "conflict_local_wins"
	ConflictRemoteWins Action = "conflict_remote_wins"
	Block              Action = "block"
)

type Decision struct {
	Action Action
	Detail string
}

func sameFile(local *Fingerprint, remote RemoteState) bool {
	return local != nil && !local.IsTombstone() && remote.Kind == File && local.Digest == remote.Digest
}

func parse(value string) *causal.Version {
	version, ok := causal.Parse(value)
	if !ok {
		return nil
	}
	return version
}

func eventIsNewer(local *Fingerprint, remote RemoteState, policy string) bool {
	localSeconds := local.MTimeNS / 1_000_000_000
	remoteSeconds := remote.MTimeNS / 1_000_000_000
	if localSeconds != remoteSeconds {
		return localSeconds > remoteSeconds
	}
	localPrecise := local.MTimeNS%1_000_000_000 != 0
	remotePrecise := remote.MTimeNS%1_000_000_000 != 0
	if localPrecise && remotePrecise && local.MTimeNS != remote.MTimeNS {
		return local.MTimeNS > remote.MTimeNS
	}
	if strings.EqualFold(policy, "newest") {
		return local.Digest > remote.Digest
	}
	return false
}

func causalWinner(local *Fingerprint, remote RemoteState, localCausal string) string {
	localVersion := parse(localCausal)
	if local != nil {
		localVersion = parse(local.Causal)
	}
	remoteVersion := parse(remote.Causal)
	if localVersion != nil && localVersion.Dominates(remoteVersion) {
		return "local"
	}
	if remoteVersion != nil && remoteVersion.Dominates(localVersion) {
		return "remote"
	}
	return ""
}

// Plan chooses one convergent action. A nil local fingerprint means the path
// is genuinely absent; callers must fail closed before reaching this function.
func Plan(
	baseline *Fingerprint, local *Fingerprint, remote RemoteState,
	localCausal string, deleteEnabled bool, policy string,
) Decision {
	if remote.Kind == Other {
		if (baseline == nil || baseline.IsTombstone()) && local == nil {
			return Decision{Nothing, "percorso NAS non regolare: ignorato"}
		}
		return Decision{Block, "sul NAS il percorso non e' un file regolare"}
	}
	localAbsent := local == nil
	baselineAbsent := baseline == nil || baseline.IsTombstone()

	if baseline == nil {
		if localAbsent {
			if remote.Kind == Tombstone {
				return Decision{Adopt, ""}
			}
			return Decision{RemoteWins, ""}
		}
		if remote.Kind == Absent {
			return Decision{Upload, ""}
		}
		if remote.Kind == Tombstone {
			return Decision{ConflictRemoteWins, "storia locale sconosciuta rispetto alla cancellazione NAS"}
		}
		if sameFile(local, remote) {
			return Decision{Adopt, ""}
		}
		switch causalWinner(local, remote, "") {
		case "local":
			return Decision{ConflictLocalWins, "versione locale causalmente successiva"}
		case "remote":
			return Decision{ConflictRemoteWins, "versione NAS causalmente successiva"}
		}
		if eventIsNewer(local, remote, policy) {
			return Decision{ConflictLocalWins, "file locale piu' recente"}
		}
		return Decision{ConflictRemoteWins, "file NAS piu' recente o contemporaneo"}
	}

	if baselineAbsent {
		if localAbsent {
			if remote.Kind == File {
				return Decision{RemoteWins, ""}
			}
			return Decision{Adopt, ""}
		}
		if remote.Kind == Absent {
			return Decision{Upload, ""}
		}
		if remote.Kind == Tombstone {
			if causalWinner(local, remote, "") == "remote" {
				return Decision{ConflictRemoteWins, "cancellazione NAS causalmente successiva"}
			}
			return Decision{Upload, "ricreazione locale successiva alla cancellazione NAS"}
		}
		if sameFile(local, remote) {
			return Decision{Adopt, ""}
		}
		switch causalWinner(local, remote, "") {
		case "local":
			return Decision{ConflictLocalWins, "versione locale causalmente successiva"}
		case "remote":
			return Decision{ConflictRemoteWins, "versione NAS causalmente successiva"}
		}
		if eventIsNewer(local, remote, policy) {
			return Decision{ConflictLocalWins, "ricreazione locale piu' recente"}
		}
		return Decision{ConflictRemoteWins, "versione NAS piu' recente o contemporanea"}
	}

	if localAbsent {
		if remote.Kind == Absent || remote.Kind == Tombstone {
			return Decision{Adopt, ""}
		}
		remoteIsBase := remote.Digest == baseline.Digest
		switch causalWinner(nil, remote, localCausal) {
		case "local":
			if deleteEnabled {
				return Decision{DeleteRemote, "cancellazione locale causalmente successiva"}
			}
		case "remote":
			return Decision{RemoteWins, "cancellazione locale stantia: versione NAS causalmente successiva"}
		}
		if deleteEnabled && remoteIsBase {
			return Decision{DeleteRemote, ""}
		}
		if !deleteEnabled {
			return Decision{RemoteWins, "propagazione cancellazioni disabilitata"}
		}
		return Decision{RemoteWins, "cancellazione locale stantia: il NAS e' cambiato"}
	}

	localChanged := local.Digest != baseline.Digest
	if remote.Kind == Absent || remote.Kind == Tombstone {
		if !localChanged {
			return Decision{RemoteWins, ""}
		}
		switch causalWinner(local, remote, local.Causal) {
		case "local":
			return Decision{Upload, "modifica locale causalmente successiva alla cancellazione NAS"}
		case "remote":
			return Decision{ConflictRemoteWins, "cancellazione NAS causalmente successiva"}
		}
		if remote.Kind == Absent || eventIsNewer(local, remote, policy) {
			return Decision{ConflictLocalWins, "modifica locale piu' recente della cancellazione NAS"}
		}
		return Decision{ConflictRemoteWins, "modifica locale concorrente con cancellazione NAS"}
	}
	if sameFile(local, remote) {
		return Decision{Adopt, ""}
	}
	remoteChanged := remote.Digest != baseline.Digest
	if localChanged && !remoteChanged {
		return Decision{Upload, ""}
	}
	if !localChanged && remoteChanged {
		return Decision{RemoteWins, ""}
	}
	if !localChanged && !remoteChanged {
		return Decision{Adopt, ""}
	}
	switch causalWinner(local, remote, local.Causal) {
	case "local":
		return Decision{ConflictLocalWins, "modifiche: versione locale causalmente successiva"}
	case "remote":
		return Decision{ConflictRemoteWins, "modifiche: versione NAS causalmente successiva"}
	}
	if eventIsNewer(local, remote, policy) {
		return Decision{ConflictLocalWins, "modifiche concorrenti: file locale piu' recente"}
	}
	return Decision{ConflictRemoteWins, "modifiche concorrenti: file NAS piu' recente o contemporaneo"}
}
