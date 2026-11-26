package engine

import (
	"os"
	"path/filepath"
	"testing"

	"nasbox-go/internal/classify"
	"nasbox-go/internal/config"
)

func TestConfirmedLiftsFolderAndBatchGuardsButNotSentinels(t *testing.T) {
	root := t.TempDir()
	cfg := config.New("")
	cfg.Set("local_root", root, false)
	cfg.Set("max_delete_files", float64(10), false)
	report := classify.Report{
		Unexplained: make([]string, 50),
		Anomalies:   []classify.Anomaly{{Dir: "Foto", Missing: 40, Remote: 40}},
	}

	if guardMessage(cfg, report, false) == "" {
		t.Fatal("senza conferma la guardia cartella deve bloccare")
	}
	if guardMessage(cfg, report, true) != "" {
		t.Fatal("con conferma la guardia cartella/batch deve lasciar passare")
	}
	if localDeleteGuard(cfg, 50, false) == "" {
		t.Fatal("senza conferma il limite di cancellazioni locali deve bloccare")
	}
	if localDeleteGuard(cfg, 50, true) != "" {
		t.Fatal("con conferma il limite di cancellazioni locali deve passare")
	}

	// Una sentinella mancante resta un blocco duro anche dopo la conferma.
	cfg.Set("delete_sentinels", ".nasbox-sentinel", false)
	if guardMessage(cfg, report, true) == "" {
		t.Fatal("la conferma non deve superare una sentinella mancante")
	}
	if err := os.WriteFile(filepath.Join(root, ".nasbox-sentinel"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if guardMessage(cfg, report, true) != "" {
		t.Fatal("sentinella presente + conferma: deve passare")
	}
}
