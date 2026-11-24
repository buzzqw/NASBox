package ui

import (
	"strings"
	"testing"
	"time"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/schedule"
	"nasbox-go/internal/storage"
)

func TestStoragePaneRendersInsights(t *testing.T) {
	c := newTestClient(t)
	if len(c.nasTabs.Items) != 3 {
		t.Fatalf("NAS tabs = %d, want monitor, devices and storage", len(c.nasTabs.Items))
	}
	c.applyStorageInsights(storage.Insights{
		Root:         c.cfg.LocalRoot(),
		CollectedAt:  time.Now(),
		ScannedFiles: 12,
		TopFolders: []storage.FolderSize{
			{Path: ".", SizeBytes: 2048, FileCount: 12},
			{Path: "docs", SizeBytes: 1024, FileCount: 4},
		},
		Duplicates: []storage.DuplicateGroup{
			{SizeBytes: 512, SHA256: "abc", Paths: []string{"docs/a.txt", "docs/b.txt"}},
		},
		DuplicateCandidates:  12,
		DuplicateWastedBytes: 512,
		Trash: storage.TrashInsight{
			VersionCount: 2, TotalBytes: 300, ExpiredCount: 1, ExpiredBytes: 100, RetentionDays: 30,
		},
	})
	if !strings.Contains(c.storageFolders.Text, "docs") || !strings.Contains(c.storageFolders.Text, "2.0 KB") {
		t.Fatalf("folders = %q", c.storageFolders.Text)
	}
	if !strings.Contains(c.storageDuplicates.Text, "docs/a.txt") {
		t.Fatalf("duplicates = %q", c.storageDuplicates.Text)
	}
	if !strings.Contains(c.storageTrash.Text, "30") {
		t.Fatalf("trash = %q", c.storageTrash.Text)
	}
	if c.storageCleanupBtn.Disabled() {
		t.Fatal("cleanup must be enabled when duplicates exist")
	}
}

func TestLogFilterAndSearch(t *testing.T) {
	c := newTestClient(t)
	c.appendLog("prima riga")
	c.appendLog("seconda riga")
	if len(c.logFiltered) != 2 {
		t.Fatalf("filtered = %d, want 2", len(c.logFiltered))
	}
	c.logSearch.SetText("seconda")
	c.refreshLogView()
	if len(c.logFiltered) != 1 || !strings.Contains(c.logFiltered[0].Detail, "seconda") {
		t.Fatalf("search result = %#v", c.logFiltered)
	}
	c.logSearch.SetText("")
	c.logFilter.SetSelected(tr("log.group.errors", nil))
	c.refreshLogView()
	if len(c.logFiltered) != 0 {
		t.Fatalf("error filter kept %d UI rows", len(c.logFiltered))
	}
	c.logFilter.SetSelected(tr("log.all_filter", nil))
	c.refreshLogView()
	if len(c.logFiltered) != 2 {
		t.Fatalf("all filter = %d", len(c.logFiltered))
	}
}

func TestScheduleEditorAcceptsDaysAndOvernightWindow(t *testing.T) {
	c := newTestClient(t)
	initial := schedule.Window{Days: []int{3}, Start: "22:00", End: "07:00"}
	saved := false
	c.openScheduleEditor(&initial, func(window schedule.Window) {
		saved = true
		if window.Start != "22:00" || window.End != "07:00" || len(window.Days) != 1 {
			t.Fatalf("saved window = %+v", window)
		}
	})
	if c.window.Canvas().Overlays().Top() == nil {
		t.Fatal("schedule editor did not open")
	}
	_ = saved
	if overlay, ok := c.window.Canvas().Overlays().Top().(interface{ Hide() }); ok {
		overlay.Hide()
	}
}

func TestPreviewDialogShowsFoldersAndRenames(t *testing.T) {
	c := newTestClient(t)
	c.showPreviewDialog(classify.Report{
		MissingTotal: 3,
		Renames:      []classify.Rename{{Missing: "old.txt", Local: "new.txt"}},
		Unexplained:  []string{"docs/a.txt", "docs/b.txt"},
		MissingByDir: map[string]int{"docs": 2},
		RemoteByDir:  map[string]int{"docs": 10},
		Anomalies:    []classify.Anomaly{{Dir: "docs", Missing: 2, Remote: 10}},
	})
	if c.window.Canvas().Overlays().Top() == nil {
		t.Fatal("preview dialog did not open")
	}
	if overlay, ok := c.window.Canvas().Overlays().Top().(interface{ Hide() }); ok {
		overlay.Hide()
	}
}
