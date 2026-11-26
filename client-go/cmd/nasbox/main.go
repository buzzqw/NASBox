// Command nasbox is the Go client's entry point. Phase 1 ships a headless
// binary so the decision engine can be tested side by side with the Python
// client before the native UI exists.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/deletesafety"
	"nasbox-go/internal/diagnostics"
	"nasbox-go/internal/discover"
	"nasbox-go/internal/engine"
	"nasbox-go/internal/history"
	"nasbox-go/internal/instance"
	"nasbox-go/internal/integration"
	"nasbox-go/internal/integrity"
	"nasbox-go/internal/ipc"
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
		os.Exit(doctor(args))
	case "diagnostics-bundle":
		os.Exit(diagnosticsBundle(args))
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
	case "find-nas":
		findNAS()
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
	case "versions":
		openVersions(args)
	case "show":
		sendOrStart("show", "")
	case "status":
		guiStatus()
	case "integrate":
		integrate(args)
	case "server-update-check":
		serverUpdate(args, false)
	case "server-update-install":
		serverUpdate(args, true)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println("uso: nasbox <version|doctor|diagnostics-bundle|preview|sync|undo|confirm-delete|browse-download|remote-history|remote-restore|prune-trash|audit|server-check|bootstrap-check|gui|update-check|update-install|server-update-check|server-update-install|versions|show|status|integrate> [opzioni]")
}

func bootstrapConfig(cfg *config.Config) error {
	client := remote.New(cfg)
	if err := client.Bootstrap(); err != nil {
		return err
	}
	return repository.Bind(cfg.LocalRoot(), cfg.RepositoryID())
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

type doctorReport struct {
	Version          string              `json:"version"`
	ConfigPath       string              `json:"config_path"`
	LocalRoot        string              `json:"local_root"`
	Repository       string              `json:"repository"`
	Tracked          int                 `json:"tracked"`
	Undo             int                 `json:"undo"`
	LastErrors       []diagnostics.Entry `json:"last_errors"`
	ServerVersion    string              `json:"server_version,omitempty"`
	ServerReachable  bool                `json:"server_reachable"`
	ServerError      string              `json:"server_error,omitempty"`
	Lock             doctorLockReport    `json:"lock"`
	Backend          string              `json:"backend,omitempty"`
	RsyncLocal       string              `json:"rsync_local,omitempty"`
	RsyncNAS         string              `json:"rsync_nas,omitempty"`
	Realtime         string              `json:"realtime,omitempty"`
	NasCapabilities  string              `json:"nas_capabilities,omitempty"`
	NasHardware      string              `json:"nas_hardware,omitempty"`
	Error            string              `json:"error,omitempty"`
	DiagnosticsError string              `json:"diagnostics_error,omitempty"`
}

type doctorLockReport struct {
	Local  diagnostics.LocalLockStatus `json:"local"`
	Remote remoteLockReport            `json:"remote"`
}

type remoteLockReport struct {
	State         string `json:"state"`
	File          string `json:"file,omitempty"`
	AgeSeconds    int64  `json:"age_seconds"`
	OwnerID       string `json:"owner_id,omitempty"`
	OwnerHost     string `json:"owner_host,omitempty"`
	OwnerPID      string `json:"owner_pid,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	Phase         string `json:"phase,omitempty"`
	ProgressDone  int64  `json:"progress_done"`
	ProgressTotal int64  `json:"progress_total"`
	Error         string `json:"error,omitempty"`
}

type serverProbe struct {
	RsyncNAS   string
	Version    string
	Reachable  bool
	Error      string
	RemoteLock remoteLockReport
}

func doctor(args []string) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "emetti un report JSON")
	cfgPath := flags.String("config", "", "percorso di client.json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "uso: nasbox doctor [--json] [--config <file>]")
		return 2
	}

	report := doctorReport{
		Version:    version.AppVersion,
		LastErrors: make([]diagnostics.Entry, 0),
		Lock: doctorLockReport{
			Remote: remoteLockReport{State: "unknown"},
		},
	}
	if *cfgPath != "" {
		report.ConfigPath = *cfgPath
	} else {
		report.ConfigPath = config.DefaultPath()
	}

	logger, logErr := diagnostics.Open()
	if logErr != nil {
		report.DiagnosticsError = logErr.Error()
	} else {
		defer logger.Close()
		entries, recentErr := recentErrors(logger)
		if recentErr != nil {
			report.DiagnosticsError = recentErr.Error()
		} else {
			report.LastErrors = entries
		}
	}
	stateDir, dirErr := diagnostics.DefaultDir()
	if logger != nil {
		stateDir = logger.Dir()
	}
	if dirErr != nil {
		report.Lock.Local = diagnostics.LocalLockStatus{State: "unknown", Error: dirErr.Error()}
	} else {
		report.Lock.Local = diagnostics.ProbeLocalLock(stateDir)
	}

	var cfg *config.Config
	var err error
	if *cfgPath != "" {
		cfg, err = config.LoadFrom(*cfgPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		report.Error = err.Error()
		if diagnosticErr := recordDiagnostic(logger, "DOCTOR_ERROR", report.ConfigPath, report.Error); diagnosticErr != nil {
			report.DiagnosticsError = diagnosticErr.Error()
		}
		if writeErr := writeDoctorReport(report, *jsonOutput); writeErr != nil {
			fmt.Fprintln(os.Stderr, writeErr)
		}
		return 1
	}

	store, err := state.Open(cfg)
	if err != nil {
		report.Error = err.Error()
	} else {
		defer store.Close()
		repositoryID := cfg.LocalRepositoryID()
		report.LocalRoot = cfg.LocalRoot()
		report.Repository = cfg.RepositoryID()
		report.Tracked = store.EntryCount(repositoryID)
		report.Undo = store.UndoCount(repositoryID, 24)
	}

	probe := probeServer(cfg)
	report.ServerVersion = probe.Version
	report.ServerReachable = probe.Reachable
	report.ServerError = probe.Error
	report.Lock.Remote = probe.RemoteLock
	report.RsyncNAS = probe.RsyncNAS
	report.Backend = cfg.String("transfer_backend")
	report.RsyncLocal = transfer.LocalRsyncVersion()
	// The probe asks the login shell, which finds the system rsync. Transfers
	// may use a newer private one that the server advertises: show both, or the
	// line would contradict the capabilities right below it.
	if private := strings.TrimSpace(cfg.String("nas_rsync_path")); private != "" {
		effective := strings.TrimSpace(cfg.String("nas_rsync_version"))
		system := strings.TrimSpace(cfg.String("nas_rsync_system_version"))
		if effective != "" {
			if system != "" && system != effective {
				report.RsyncNAS = effective + " privato, sistema " + system
			} else {
				report.RsyncNAS = effective + " privato"
			}
		}
	}
	report.Realtime = engine.RealtimeSummary(cfg)
	report.NasCapabilities = cfg.String("nas_capabilities")
	report.NasHardware = engine.CapabilitiesSummary(cfg)
	if !probe.Reachable && probe.Error != "" {
		report.Lock.Remote.Error = probe.Error
	}

	action := "DOCTOR"
	if report.Error != "" {
		action = "DOCTOR_ERROR"
	}
	if diagnosticErr := recordDiagnostic(logger, action, report.ConfigPath, report.Error); diagnosticErr != nil {
		report.DiagnosticsError = diagnosticErr.Error()
	}
	if err := writeDoctorReport(report, *jsonOutput); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if report.Error != "" {
		return 1
	}
	return 0
}

// doctorErrorWindow limits "ultimi errori" to recent problems: older entries
// (for example from the legacy Python client sharing client.log) are history,
// not the current state, and only produced false alarms.
const doctorErrorWindow = 24 * time.Hour

func recentErrors(logger *diagnostics.Logger) ([]diagnostics.Entry, error) {
	if logger == nil {
		return make([]diagnostics.Entry, 0), nil
	}
	entries, err := logger.Recent(2000)
	if err != nil {
		return nil, err
	}
	return summarizeErrors(entries, time.Now().Add(-doctorErrorWindow), 10), nil
}

// summarizeErrors returns the newest distinct ERROR entries after since. A
// message repeated many times (a failing tick) appears once, with the number
// of repetitions appended to its detail.
func summarizeErrors(entries []diagnostics.Entry, since time.Time, limit int) []diagnostics.Entry {
	result := make([]diagnostics.Entry, 0)
	position := map[string]int{}
	counts := map[string]int{}
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.Level != "ERROR" || entry.Timestamp.Before(since) {
			continue
		}
		key := entry.Action + "\x00" + entry.Path + "\x00" + strings.Join(strings.Fields(entry.Detail), " ")
		if _, seen := position[key]; seen {
			counts[key]++
			continue
		}
		if len(result) >= limit {
			continue
		}
		position[key] = len(result)
		counts[key] = 1
		result = append(result, entry)
	}
	for key, at := range position {
		if counts[key] > 1 {
			result[at].Detail = fmt.Sprintf("%s (×%d)", result[at].Detail, counts[key])
		}
	}
	return result
}

func probeServer(cfg *config.Config) serverProbe {
	probe := serverProbe{
		RemoteLock: remoteLockReport{State: "unknown"},
	}
	if cfg == nil {
		probe.Error = "configurazione non disponibile"
		return probe
	}
	if strings.TrimSpace(cfg.String("nas_lan")) == "" &&
		strings.TrimSpace(cfg.String("nas_wan")) == "" &&
		strings.TrimSpace(cfg.String("jump_host")) == "" {
		probe.Error = "NAS non configurato"
		return probe
	}
	if strings.TrimSpace(cfg.RemoteScript()) == "" {
		probe.Error = "script server non configurato"
		return probe
	}
	client, err := remote.NewResolved(cfg)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	values, err := client.PrintConfig()
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.Reachable = true
	probe.Version = strings.TrimSpace(values["VERSION"])
	if probe.Version == "" {
		probe.Error = "versione server sconosciuta"
	}
	probe.RemoteLock = remoteLockFromValues(values)
	if output, err := client.Run([]string{"rsync", "--version"}, nil, 20*time.Second); err == nil {
		probe.RsyncNAS = transfer.ParseRsyncVersion(string(output))
	}
	return probe
}

func remoteLockFromValues(values map[string]string) remoteLockReport {
	status := remoteLockReport{
		State:     "unknown",
		File:      values["SYNC_LOCK_FILE"],
		OwnerID:   values["SYNC_LOCK_OWNER_ID"],
		OwnerHost: values["SYNC_LOCK_OWNER_HOST"],
		OwnerPID:  values["SYNC_LOCK_OWNER_PID"],
		StartedAt: values["SYNC_LOCK_STARTED_AT"],
		Phase:     values["SYNC_LOCK_PHASE"],
	}
	switch strings.ToLower(strings.TrimSpace(values["SYNC_LOCK_HELD"])) {
	case "true":
		status.State = "held"
	case "false":
		status.State = "free"
	}
	status.AgeSeconds, _ = strconv.ParseInt(values["SYNC_LOCK_AGE_SECONDS"], 10, 64)
	status.ProgressDone, _ = strconv.ParseInt(values["SYNC_LOCK_PROGRESS_DONE"], 10, 64)
	status.ProgressTotal, _ = strconv.ParseInt(values["SYNC_LOCK_PROGRESS_TOTAL"], 10, 64)
	return status
}

func recordDiagnostic(logger *diagnostics.Logger, action, path, detail string) error {
	if logger == nil {
		return nil
	}
	if err := logger.Log(action, path, detail); err != nil {
		return err
	}
	return logger.Event(action, path, detail)
}

func writeDoctorReport(report doctorReport, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	fmt.Printf("version:     %s\n", report.Version)
	fmt.Printf("config:      %s\n", report.ConfigPath)
	fmt.Printf("local root:  %s\n", report.LocalRoot)
	fmt.Printf("repository:  %s\n", report.Repository)
	fmt.Printf("tracked:     %d\n", report.Tracked)
	fmt.Printf("undo count:  %d\n", report.Undo)
	if len(report.LastErrors) == 0 {
		fmt.Println("ultimi errori: nessuno nelle ultime 24 ore")
	} else {
		fmt.Println("ultimi errori (24 ore):")
		for _, entry := range report.LastErrors {
			fmt.Printf("  %s [%s] %s %s %s\n",
				entry.Timestamp.Format(time.RFC3339), entry.Level, entry.Action,
				entry.Path, strings.Join(strings.Fields(entry.Detail), " "))
		}
	}
	if report.ServerReachable {
		fmt.Printf("server:      %s (raggiungibile)\n", report.ServerVersion)
	} else {
		fmt.Printf("server:      non raggiungibile: %s\n", report.ServerError)
	}
	fmt.Printf("lock locale: %s", report.Lock.Local.State)
	if report.Lock.Local.Error != "" {
		fmt.Printf(" (%s)", report.Lock.Local.Error)
	}
	fmt.Println()
	fmt.Printf("lock NAS:    %s\n", formatRemoteLock(report.Lock.Remote))
	fmt.Printf("backend:     %s\n", valueOr(report.Backend, "rsync"))
	fmt.Printf("rsync:       locale %s · NAS %s (non usato per i trasferimenti se backend=chunks)\n", valueOr(report.RsyncLocal, "?"), valueOr(report.RsyncNAS, "?"))
	fmt.Printf("tempo reale: %s\n", valueOr(report.Realtime, "?"))
	fmt.Printf("capacità NAS: %s\n", valueOr(report.NasHardware, "?"))
	if report.Error != "" {
		fmt.Println("errore:", report.Error)
	}
	if report.DiagnosticsError != "" {
		fmt.Println("diagnostica:", report.DiagnosticsError)
	}
	return nil
}

func formatRemoteLock(status remoteLockReport) string {
	if status.State == "held" {
		owner := strings.Join(nonempty(status.OwnerID, status.OwnerHost, status.OwnerPID), " / ")
		if owner == "" {
			owner = "sconosciuto"
		}
		if status.Phase != "" {
			owner += " (" + status.Phase + ")"
		}
		if status.ProgressTotal > 0 {
			owner += fmt.Sprintf(" %d/%d", status.ProgressDone, status.ProgressTotal)
		}
		return fmt.Sprintf("occupato da %s da %d secondi", owner, status.AgeSeconds)
	}
	if status.Error != "" {
		return status.State + ": " + status.Error
	}
	return status.State
}

func nonempty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func diagnosticsBundle(args []string) int {
	flags := flag.NewFlagSet("diagnostics-bundle", flag.ContinueOnError)
	output := flags.String("output", "", "percorso del tar.gz da creare")
	cfgPath := flags.String("config", "", "percorso di client.json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*output) == "" {
		fmt.Fprintln(os.Stderr, "uso: nasbox diagnostics-bundle --output <file> [--config <file>]")
		return 2
	}

	stateDir, dirErr := diagnostics.DefaultDir()
	logger, logErr := diagnostics.Open()
	warnings := make([]string, 0)
	if logErr != nil {
		warnings = append(warnings, "logger diagnostico non disponibile: "+logErr.Error())
		fmt.Fprintln(os.Stderr, "avviso:", logErr)
	} else {
		defer logger.Close()
		stateDir = logger.Dir()
	}
	if dirErr != nil {
		fmt.Fprintln(os.Stderr, "directory diagnostica:", dirErr)
		return 1
	}

	configPath := *cfgPath
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	var cfg *config.Config
	var err error
	if *cfgPath != "" {
		cfg, err = config.LoadFrom(*cfgPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		warnings = append(warnings, "configurazione non leggibile: "+err.Error())
		fmt.Fprintln(os.Stderr, "avviso: configurazione non leggibile:", err)
	}

	versions := map[string]any{
		"client_name":    version.AppName,
		"client_version": version.AppVersion,
		"go_version":     runtime.Version(),
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"config_path":    configPath,
	}
	if cfg != nil {
		versions["minimum_server_version"] = cfg.String("min_server_version")
		probe := probeServer(cfg)
		versions["server_version"] = probe.Version
		versions["server_reachable"] = probe.Reachable
		if probe.Error != "" {
			versions["server_error"] = probe.Error
		}
	} else {
		versions["server_reachable"] = false
	}

	if diagnosticErr := recordDiagnostic(logger, "DIAGNOSTICS_BUNDLE_START", *output, configPath); diagnosticErr != nil {
		warnings = append(warnings, "scrittura evento di avvio non riuscita: "+diagnosticErr.Error())
	}
	err = diagnostics.CreateBundle(diagnostics.BundleOptions{
		Output:     *output,
		StateDir:   stateDir,
		ConfigPath: configPath,
		Versions:   versions,
		Warnings:   warnings,
	})
	if err != nil {
		_ = recordDiagnostic(logger, "DIAGNOSTICS_BUNDLE_ERROR", *output, err.Error())
		fmt.Fprintln(os.Stderr, "creazione bundle diagnostico fallita:", err)
		return 1
	}
	if diagnosticErr := recordDiagnostic(logger, "DIAGNOSTICS_BUNDLE", *output, "bundle tar.gz redatto creato"); diagnosticErr != nil {
		fmt.Fprintln(os.Stderr, "avviso: bundle creato ma evento non registrabile:", diagnosticErr)
	}
	fmt.Println("bundle diagnostico:", *output)
	return 0
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
	if err := transfer.NewForConfig(cfg, client).DownloadPath(args[0]); err != nil {
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

// findNAS lists the machines on the local network that look like a NAS.
func findNAS() {
	candidates := discover.Find(context.Background(), 5*time.Second)
	if len(candidates) == 0 {
		fmt.Println("nessun NAS trovato in rete")
		return
	}
	for _, candidate := range candidates {
		banner := candidate.Banner
		if banner == "" {
			banner = "-"
		}
		fmt.Printf("%-18s %-24s mdns=%-5v ssh=%s\n", candidate.IP, candidate.Name, candidate.MDNS, banner)
	}
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
		// Already running: bring the existing window forward instead of
		// failing, as people expect when they reopen a sync app.
		if _, sendErr := ipc.Send("show", ""); sendErr == nil {
			return
		}
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
	if cfg.LocalRoot() != "" {
		if err := bootstrapConfig(cfg); err != nil {
			// A PC already bound to a repository starts anyway (NAS off,
			// laptop away from home): the GUI shows "NAS non raggiungibile"
			// and the engine retries. Only a never-bootstrapped setup stops.
			if cfg.RepositoryID() == "" {
				fmt.Fprintln(os.Stderr, "bootstrap NAS fallito:", err)
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "NAS non raggiungibile all'avvio, riprovo in background:", err)
		}
	}
	var store *state.Store
	if cfg.LocalRoot() != "" {
		opened, err := state.Open(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		store = opened
		defer store.Close()
	}
	logger, logErr := diagnostics.Open()
	if logErr != nil {
		fmt.Fprintln(os.Stderr, "diagnostica non disponibile:", logErr)
	} else {
		defer logger.Close()
	}
	ui.Run(cfg, store, logger)
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
	transfers := transfer.NewForConfig(cfg, client)
	// Print every transferred file, as the GUI's activity list does; this
	// also exercises the same rsync progress stream from the command line.
	transfers.SetObservers(nil, func(event transfer.FileEvent) {
		arrow := "↓"
		if event.Upload {
			arrow = "↑"
		}
		fmt.Println(arrow, event.Path)
	})
	result, err := engine.SyncOnce(cfg, store, client, transfers)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if result.Blocked != "" {
		fmt.Println("BLOCCATO:", result.Blocked)
		return
	}
	fmt.Printf("caricati %d, scaricati %d, cancellati sul NAS %d, cancellati in locale %d, spostamenti rilevati %d, rinominati in locale %d, conflitti %d, saltati %d\n",
		result.Uploaded, result.Downloads, result.Deleted, result.LocalDeleted, result.Renames,
		result.LocalRenamed, result.Conflicts, result.Stale)
	if result.PrunedExcluded > 0 {
		fmt.Printf("%d voci di stato eliminate perché ora escluse dalle regole\n", result.PrunedExcluded)
	}
	if result.Local != nil && len(result.Local.Skipped) > 0 {
		fmt.Printf("%d link simbolici o file speciali ignorati\n", len(result.Local.Skipped))
	}
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

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// serverUpdate finds (and optionally installs) a newer server script
// published on the NAS as sync-daemon-server-<version>.sh.
func serverUpdate(args []string, install bool) {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client := remote.New(cfg)
	values, err := client.PrintConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	current := values["VERSION"]
	update, err := client.FindServerUpdate(current)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if update == nil {
		fmt.Printf("server %s: nessun aggiornamento\n", current)
		return
	}
	fmt.Printf("server %s: disponibile %s (%s)\n", current, update.Version, update.Path)
	if !install {
		return
	}
	if err := client.InstallServerUpdate(*update); err != nil {
		fmt.Fprintln(os.Stderr, "aggiornamento server fallito:", err)
		os.Exit(1)
	}
	fmt.Printf("server aggiornato a %s\n", update.Version)
}

// openVersions is what file-manager menus call: "versions <file>".
func openVersions(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "uso: nasbox versions <file>")
		os.Exit(2)
	}
	cfg, err := loadConfig(args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	relative, err := relativeToRoot(cfg.LocalRoot(), args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sendOrStart("versions", relative)
}

// relativeToRoot maps an absolute (or relative) path inside the NASBox folder
// to the repository path; symlinks in the prefix are resolved.
func relativeToRoot(root, target string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("cartella NASBox non configurata")
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(absolute)); err == nil {
		absolute = filepath.Join(resolved, filepath.Base(absolute))
	}
	base := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		base = resolved
	}
	relative, err := filepath.Rel(base, absolute)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return "", fmt.Errorf("%s non e' dentro la cartella NASBox (%s)", target, root)
	}
	return filepath.ToSlash(relative), nil
}

// sendOrStart delivers a command to the running GUI or starts it with the
// command queued (NASBOX_OPEN_VERSIONS) when it is not running.
func sendOrStart(command, argument string) {
	reply, err := ipc.Send(command, argument)
	if err == nil {
		if reply != "" && reply != "ok" {
			fmt.Println(reply)
		}
		return
	}
	binary, exeErr := os.Executable()
	if exeErr != nil {
		fmt.Fprintln(os.Stderr, exeErr)
		os.Exit(1)
	}
	start := exec.Command(binary, "gui")
	start.Env = os.Environ()
	if command == "versions" {
		start.Env = append(start.Env, "NASBOX_OPEN_VERSIONS="+argument)
	}
	start.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := start.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func guiStatus() {
	reply, err := ipc.Send("status", "")
	if err != nil {
		fmt.Println(err)
		os.Exit(3)
	}
	fmt.Println(reply)
}

// integrate installs (or with --remove removes) the file-manager entries.
func integrate(args []string) {
	remove := len(args) > 0 && args[0] == "--remove"
	var result integration.Result
	var err error
	if remove {
		result, err = integration.Remove()
	} else {
		binary, exeErr := os.Executable()
		if exeErr != nil {
			fmt.Fprintln(os.Stderr, exeErr)
			os.Exit(1)
		}
		result, err = integration.Install(binary)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("aggiornati: %s\n", strings.Join(result.Changed, ", "))
	if len(result.Skipped) > 0 {
		fmt.Printf("non installati: %s\n", strings.Join(result.Skipped, ", "))
	}
}
