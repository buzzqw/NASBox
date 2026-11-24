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
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println("uso: nasbox <version|doctor|preview> [opzioni]")
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
