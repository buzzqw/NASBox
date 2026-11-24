// Command nasbox is the Go client's entry point. Phase 1 ships a headless
// binary so the decision engine can be tested side by side with the Python
// client before the native UI exists.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/deletesafety"
	"nasbox-go/internal/engine"
	"nasbox-go/internal/history"
	"nasbox-go/internal/instance"
	"nasbox-go/internal/integrity"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/repository"
	"nasbox-go/internal/state"
	"nasbox-go/internal/transfer"
	"nasbox-go/internal/ui"
	"nasbox-go/internal/update"
	"nasbox-go/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	command := os.Args[1]
	args := os.Args[2:]
	switch command {
	case "version":
		fmt.Printf("%s %s\n", version.AppName, version.AppVersion)
	case "doctor":
		doctor(args)
	case "preview":
		preview(args)
	case "sync":
		syncOnce(args)
	case "undo":
		undoDeletions(args)
	case "confirm-delete":
		confirmDelete(args)
	case "browse-download":
		browseDownload(args)
	case "remote-history":
		remoteHistory(args)
	case "remote-restore":
		remoteRestore(args)
	case "prune-trash":
		pruneTrash(args)
	case "audit":
		runAudit(args)
	case "server-check":
		serverCheck(args)
	case "bootstrap-check":
		bootstrapCheck(args)
	case "gui":
		gui(args)
	case "update-check":
		updateCheck(args)
	case "update-install":
		updateInstall(args)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println("uso: nasbox <version|doctor|preview|sync|undo|confirm-delete|browse-download|remote-history|remote-restore|prune-trash|audit|server-check|bootstrap-check|gui|update-check|update-install> [opzioni]")
}

func bootstrapConfig(cfg *config.Config) error {
	client := remote.New(cfg)
	if err := client.Bootstrap(); err != nil {
		return err
	}
	return repository.Bind(cfg.LocalRoot(), cfg.RepositoryID())
}

// serverInfoClient adds the server-version lookup required by the updater's
// requires_server validation to the ordinary SSH client.
type serverInfoClient struct{ *remote.Client }

func (c serverInfoClient) ServerVersion() (string, error) {
	_, current, err := c.ServerOutdated("0.0.0")
	return current, err
}

// startupUpdateRecovery runs before gui/sync so an interrupted update is
// repaired and a successfully started binary is confirmed. A rollback forces
// a restart; a first failed health check is only reported.
func startupUpdateRecovery() error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if err := update.RollbackIfNeeded(binary); err != nil {
		fmt.Fprintln(os.Stderr, "recupero aggiornamento:", err)
		if errors.Is(err, update.ErrRollbackPerformed) {
			return err
		}
	}
	if err := update.ConfirmStartedOK(binary); err != nil {
		fmt.Fprintln(os.Stderr, "verifica aggiornamento:", err)
		if errors.Is(err, update.ErrRollbackPerformed) {
			return err
		}
	}
	return nil
}

func loadConfig(args []string) (*config.Config, error) {
	flags := flag.NewFlagSet("config", flag.ContinueOnError)
	path := flags.String("config", "", "percorso di client.json")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if *path != "" {
		return config.LoadFrom(*path)
	}
	return config.Load()
}

func doctor(args []string) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store, err := state.Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer store.Close()
	fmt.Printf("config:      %s\n", cfg.Path)
	fmt.Printf("local root:  %s\n", cfg.LocalRoot())
	fmt.Printf("repository:  %s\n", cfg.RepositoryID())
	fmt.Printf("tracked:     %d\n", store.EntryCount(cfg.LocalRepositoryID()))
	fmt.Printf("undo count:  %d\n", store.UndoCount(cfg.LocalRepositoryID(), 24))
}

func updateCheck(args []string) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	manifest, err := update.Newest(cfg, serverInfoClient{remote.New(cfg)})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("canale Go: %s %s\n", manifest.Channel, manifest.Version)
	if update.Available(manifest) {
		fmt.Printf("aggiornamento disponibile: %s -> %s\n", version.AppVersion, manifest.Version)
		if manifest.Notes != "" {
			fmt.Println(manifest.Notes)
		}
	} else {
		fmt.Println("nessun aggiornamento")
	}
}

func updateInstall(args []string) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	applied, versionName, err := update.Apply(cfg, serverInfoClient{remote.New(cfg)}, binary)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if applied {
		fmt.Printf("aggiornato a %s — riavvia il client\n", versionName)
	} else {
		fmt.Println("nessun aggiornamento")
	}
}

func browseDownload(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "uso: nasbox browse-download <percorso-remoto>")
		os.Exit(2)
	}
	cfg, err := loadConfig(args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client := remote.New(cfg)
	store, err := state.Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer store.Close()
	if err := transfer.New(cfg, client).DownloadPath(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("scaricato:", args[0])
}

func remoteHistory(args []string) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	versions, err := remote.New(cfg).HistoryList()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, version := range versions {
		fmt.Printf("%s\t%s\t%d\n", version.Original, version.Suffix, version.Size)
	}
}

func remoteRestore(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "uso: nasbox remote-restore <percorso> <suffisso>")
		os.Exit(2)
	}
	cfg, err := loadConfig(args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store, err := state.Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer store.Close()
	status, err := remote.New(cfg).RestoreVersion(args[0], args[1], store.DeviceID())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(status)
}

func runAudit(args []string) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client := remote.New(cfg)
	raw, err := client.ManifestExport("")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	manifest, err := remote.ParseManifest(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report := integrity.Run(cfg, manifest, version.AppVersion, nil)
	fmt.Printf("%s: %d file, %d differenze\n", report.Status, report.FilesChecked, len(report.Differences))
}

func bootstrapCheck(args []string) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := bootstrapConfig(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("bootstrap NAS ok")
}

func serverCheck(args []string) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	minimum := cfg.String("min_server_version")
	if minimum == "" {
		minimum = "3.16.0"
	}
	outdated, current, err := remote.New(cfg).ServerOutdated(minimum)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if outdated {
		fmt.Printf("server obsoleto: %s (richiesto %s)\n", current, minimum)
	} else {
		fmt.Printf("server ok: %s\n", current)
	}
}

func pruneTrash(args []string) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	removed, err := history.Prune(cfg.Int("retention_days_local"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("rimossi %d file dallo storico locale\n", removed)
}

func confirmDelete(args []string) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	until, err := deletesafety.Shared().Confirm(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("cancellazioni di massa confermate fino a %s\n", time.Unix(int64(until), 0).Format(time.RFC3339))
}

func gui(args []string) {
	release, err := instance.Acquire()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer release()
	if err := startupUpdateRecovery(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if cfg.LocalRoot() == "" {
		fmt.Fprintln(os.Stderr, "cartella locale non configurata: imposta local_root in client.json")
		os.Exit(1)
	}
	if err := bootstrapConfig(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "bootstrap NAS fallito:", err)
		os.Exit(1)
	}
	store, err := state.Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer store.Close()
	ui.Run(cfg, store)
}

func syncOnce(args []string) {
	if err := startupUpdateRecovery(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := bootstrapConfig(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "bootstrap NAS fallito:", err)
		os.Exit(1)
	}
	store, err := state.Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer store.Close()
	client := remote.New(cfg)
	result, err := engine.SyncOnce(cfg, store, client, transfer.New(cfg, client))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if result.Blocked != "" {
		fmt.Println("BLOCCATO:", result.Blocked)
		return
	}
	fmt.Printf("caricati %d, cancellati %d, spostamenti rilevati %d\n",
		result.Uploaded, result.Deleted, result.Renames)
}

func undoDeletions(args []string) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store, err := state.Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer store.Close()
	entries, err := store.RecentUndo(cfg.LocalRepositoryID(), 24, 500)
	if err != nil || len(entries) == 0 {
		fmt.Println("nessuna eliminazione recente da annullare")
		return
	}
	batches := map[string][]remote.RestoreRequest{}
	for _, item := range entries {
		batches[item.BatchID] = append(batches[item.BatchID], remote.RestoreRequest{
			Path: item.Path, VersionTS: item.VersionTS,
		})
	}
	client := remote.New(cfg)
	device := store.DeviceID()
	restored := 0
	for batchID, requests := range batches {
		statuses, err := client.RestoreBatch(requests, device)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		done := []string{}
		for path, status := range statuses {
			if status == "RESTORED" {
				done = append(done, path)
			}
		}
		if len(done) > 0 {
			_ = store.MarkUndoApplied(cfg.LocalRepositoryID(), batchID, done)
			restored += len(done)
		}
	}
	fmt.Printf("recuperati %d file dal cestino NAS\n", restored)
}

func preview(args []string) {
	flags := flag.NewFlagSet("preview", flag.ContinueOnError)
	manifestPath := flags.String("manifest", "", "file con l'output di --manifest-export")
	cfgPath := flags.String("config", "", "percorso di client.json")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "serve --manifest <file> (per ora il Go client non scarica ancora dal NAS)")
		os.Exit(2)
	}
	var cfg *config.Config
	var err error
	if *cfgPath != "" {
		cfg, err = config.LoadFrom(*cfgPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	content, err := os.ReadFile(*manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	manifest, err := remote.ParseManifest(content)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store, err := state.Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer store.Close()
	report, err := engine.Preview(cfg, store, manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(report.Summary())
	for _, anomaly := range report.Anomalies {
		fmt.Printf("ANOMALIA %s: %d/%d (%.0f%%)\n",
			anomaly.Dir, anomaly.Missing, anomaly.Remote, anomaly.Ratio()*100)
	}
	for _, rename := range report.Renames {
		fmt.Printf("RENAME %s -> %s\n", rename.Missing, rename.Local)
	}
}
