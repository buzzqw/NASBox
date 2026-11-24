// Command nasbox is the Go client's entry point. Phase 1 ships a headless
// binary so the decision engine can be tested side by side with the Python
// client before the native UI exists.
package main

import (
	"flag"
	"fmt"
	"os"

	"nasbox-go/internal/config"
	"nasbox-go/internal/engine"
	"nasbox-go/internal/remote"
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
	fmt.Println("uso: nasbox <version|doctor|preview|sync|undo|gui|update-check|update-install> [opzioni]")
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
	fmt.Printf("tracked:     %d\n", store.EntryCount(cfg.RepositoryID()))
	fmt.Printf("undo count:  %d\n", store.UndoCount(cfg.RepositoryID(), 24))
}

func updateCheck(args []string) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	manifest, err := update.Newest(cfg, remote.New(cfg))
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
	applied, versionName, err := update.Apply(cfg, remote.New(cfg), binary)
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

func gui(args []string) {
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
	ui.Run(cfg, store)
}

func syncOnce(args []string) {
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
	entries, err := store.RecentUndo(cfg.RepositoryID(), 24, 500)
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
			_ = store.MarkUndoApplied(cfg.RepositoryID(), batchID, done)
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
