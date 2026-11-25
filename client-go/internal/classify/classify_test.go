package classify

import "testing"

func TestRenameIsNotADeletion(t *testing.T) {
	remote := map[string]RemoteState{
		"old/a.png": {Kind: "FILE", Digest: "abc", Size: 10},
	}
	identity := map[string]Identity{
		"new/b.png": {Digest: "abc", Size: 10, CtimeNS: 123},
	}
	report := Classify([]string{"old/a.png"}, remote, identity, 0.5, 20)
	if len(report.Renames) != 1 || len(report.Unexplained) != 0 {
		t.Fatalf("expected one rename and no deletion, got %+v", report)
	}
	if report.Renames[0].Local != "new/b.png" {
		t.Fatalf("wrong rename target: %+v", report.Renames[0])
	}
}

func TestWholeDirectoryAnomaly(t *testing.T) {
	remote := make(map[string]RemoteState)
	missing := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		path := "OptAlarm/file.txt"
		if i > 0 {
			path = "OptAlarm/file" + string(rune('a'+i)) + ".txt"
		}
		remote[path] = RemoteState{Kind: "FILE", Digest: "d", Size: 1}
		missing = append(missing, path)
	}
	report := Classify(missing, remote, map[string]Identity{}, 0.5, 20)
	if len(report.Anomalies) != 1 || report.Anomalies[0].Dir != "OptAlarm" {
		t.Fatalf("expected OptAlarm anomaly, got %+v", report.Anomalies)
	}
	if report.Anomalies[0].Ratio() != 1.0 {
		t.Fatalf("expected 100%% loss, got %v", report.Anomalies[0].Ratio())
	}
}

// A local file that the NAS already tracks is not a rename target: the server
// refuses it and the refusal used to block every sync pass.
func TestTrackedDuplicateIsNotARenameTarget(t *testing.T) {
	remote := map[string]RemoteState{
		"SADE/SADE/images/a.png": {Kind: "FILE", Digest: "d1", Size: 10},
		"SADE/images/a.png":      {Kind: "FILE", Digest: "d1", Size: 10},
	}
	identity := map[string]Identity{"SADE/images/a.png": {Digest: "d1", Size: 10}}
	report := Classify([]string{"SADE/SADE/images/a.png"}, remote, identity, 0.5, 20)
	if len(report.Renames) != 0 || len(report.Unexplained) != 1 {
		t.Fatalf("report: %+v", report)
	}
	// A tombstoned destination is still a valid rename target.
	remote["SADE/images/a.png"] = RemoteState{Kind: "TOMBSTONE"}
	report = Classify([]string{"SADE/SADE/images/a.png"}, remote, identity, 0.5, 20)
	if len(report.Renames) != 1 {
		t.Fatalf("tombstone target: %+v", report)
	}
}
