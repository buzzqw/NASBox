package serverd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func runTrim(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// dfFields returns the data row of "df -Pk <path>", not the header.
func dfFields(root string) []string {
	lines := strings.Split(runTrim("df", "-Pk", root), "\n")
	if len(lines) < 2 {
		return nil
	}
	return strings.Fields(lines[1])
}

func capabilityValues(ctx *Context) []string {
	cores := 1
	if raw := runTrim("nproc"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			cores = n
		}
	}
	memMB := 0
	if file, err := os.Open("/proc/meminfo"); err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 2 && fields[0] == "MemTotal:" {
				if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					memMB = int(kb / 1024)
				}
				break
			}
		}
		file.Close()
	}
	rotational := diskRotational(ctx.ShareRoot)
	systemVersion := rsyncVersion("rsync")
	privatePath := filepath.Join(ctx.ServerDir, "bin", "nasbox-rsync")
	privateVersion := rsyncVersion(privatePath)
	caps := "change_feed,causal_versions,v3_rename,batch_delete,quiet_lock,empty_dir_prune,capabilities_v1,chunk_transfer_v1,chunk_transfer_v2,chunk_transfer_v3"
	if privateVersion != "" {
		caps += ",private_rsync"
	}
	rsyncPath := ""
	if privateVersion != "" {
		rsyncPath = privatePath
	}
	rsyncVer := systemVersion
	if privateVersion != "" {
		rsyncVer = privateVersion
	}
	parallel := cores
	if parallel > 8 {
		parallel = 8
	}
	if parallel < 1 {
		parallel = 1
	}
	if raw := strings.Fields(ctx.Config["FILE_STATES_PARALLELISM"]); len(raw) > 0 {
		if n, err := strconv.Atoi(raw[0]); err == nil && n > 0 {
			parallel = n
		}
	}
	return []string{
		"CAPABILITIES=" + caps,
		"CHANGE_FEED_AVAILABLE=true",
		"CAUSAL_VERSIONS_AVAILABLE=true",
		"CHUNK_TRANSFER_AVAILABLE=true",
		"CHUNK_TRANSFER_VERSION=3",
		"BROWSE_RENAME_PROTOCOLS=BROWSE_RENAME_V1,BROWSE_RENAME_V2,BROWSE_RENAME_V3",
		fmt.Sprintf("CPU_COUNT=%d", cores),
		fmt.Sprintf("MEM_TOTAL_MB=%d", memMB),
		fmt.Sprintf("DISK_ROTATIONAL=%s", rotational),
		"RSYNC_VERSION=" + rsyncVer,
		"RSYNC_PATH=" + rsyncPath,
		"RSYNC_SYSTEM_VERSION=" + systemVersion,
		"FS_TYPE=" + fsType(ctx.ShareRoot),
		fmt.Sprintf("FILE_STATES_PARALLELISM=%d", parallel),
	}
}

func rsyncVersion(binary string) string {
	out := runTrim(binary, "--version")
	if out == "" {
		return ""
	}
	first := strings.SplitN(out, "\n", 2)[0]
	fields := strings.Fields(first)
	if len(fields) < 3 {
		return ""
	}
	version := fields[2]
	if index := strings.IndexFunc(version, func(r rune) bool {
		return (r < '0' || r > '9') && r != '.'
	}); index >= 0 {
		version = version[:index]
	}
	return version
}

func diskRotational(root string) string {
	df := runTrim("df", "-P", root)
	lines := strings.Split(df, "\n")
	if len(lines) < 2 {
		return ""
	}
	device := strings.Fields(lines[1])[0]
	resolved := runTrim("readlink", "-f", device)
	if resolved == "" {
		resolved = device
	}
	base := filepath.Base(resolved)
	candidates := []string{
		filepath.Join("/sys/block", base, "queue", "rotational"),
	}
	matches, _ := filepath.Glob(filepath.Join("/sys/block", "*", base, "queue", "rotational"))
	candidates = append(candidates, matches...)
	sd, _ := filepath.Glob("/sys/block/sd*/queue/rotational")
	candidates = append(candidates, sd...)
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}

func fsType(root string) string {
	df := runTrim("df", "-P", root)
	lines := strings.Split(df, "\n")
	if len(lines) < 2 {
		return ""
	}
	mount := strings.Fields(lines[1])[5]
	if file, err := os.Open("/proc/mounts"); err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 3 && fields[1] == mount {
				file.Close()
				return fields[2]
			}
		}
		file.Close()
	}
	return runTrim("stat", "-f", "-c", "%T", root)
}

func cmdCapabilities(ctx *Context) error {
	fmt.Println("CAPABILITIES_V1")
	fmt.Println("SERVER_VERSION=" + version)
	for _, line := range capabilityValues(ctx) {
		fmt.Println(line)
	}
	return nil
}

func repositoryID(root string) string {
	file, err := os.Open(filepath.Join(root, ".nasbox-root"))
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() || scanner.Text() != "NASBOX_REPOSITORY_V1" {
		return ""
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "ID=") {
			id := strings.TrimPrefix(line, "ID=")
			if regexp.MustCompile(`^[A-Za-z0-9._:-]{16,128}$`).MatchString(id) {
				return id
			}
			return ""
		}
	}
	return ""
}

type lockState struct {
	Held  bool
	PID   string
	ID    string
	Host  string
	Since string
	Phase string
	Done  string
	Total string
	Age   string
}

func (c *Context) readLock() lockState {
	p := c.paths()
	state := lockState{Since: "0", Done: "0", Total: "0"}
	file, err := os.OpenFile(p.Lock, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		return state
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		state.Held = true
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	if !state.Held {
		return state
	}
	data, err := os.ReadFile(p.Owner)
	if err != nil {
		return state
	}
	fields := strings.Split(strings.TrimSpace(string(data)), "|")
	if len(fields) > 0 {
		state.ID = fields[0]
	}
	if len(fields) > 1 {
		state.Host = fields[1]
	}
	if len(fields) > 2 && regexp.MustCompile(`^[0-9]+$`).MatchString(fields[2]) {
		state.Since = fields[2]
	}
	if len(fields) > 3 {
		state.Phase = fields[3]
	}
	if len(fields) > 4 && regexp.MustCompile(`^[0-9]+$`).MatchString(fields[4]) {
		state.Done = fields[4]
	}
	if len(fields) > 5 && regexp.MustCompile(`^[0-9]+$`).MatchString(fields[5]) {
		state.Total = fields[5]
	}
	if pid, host := lockOwningProcess(p.Lock); pid != "" {
		state.PID = pid
		if state.Host == "" {
			state.Host = host
		}
	}
	return state
}

func lockOwningProcess(lock string) (pid, host string) {
	out := runTrim("ps", "-eo", "pid=,etimes=,args=")
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, lock) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return fields[0], fields[len(fields)-1]
		}
	}
	return "", ""
}

func cmdPrintConfig(ctx *Context) error {
	p := ctx.paths()
	id := repositoryID(ctx.ShareRoot)
	running := false
	if raw := readString(p.DaemonPID); raw != "" {
		cmdline, err := os.ReadFile(filepath.Join("/proc", raw, "cmdline"))
		if err == nil {
			text := strings.ReplaceAll(string(cmdline), "\x00", " ")
			running = strings.Contains(text, ctx.ScriptPath) && strings.Contains(text, "--run-foreground")
		}
	}
	trashBytes, trashFiles, trashAt := "", "", ""
	if raw := readString(p.TrashUsage); raw != "" {
		fields := strings.Fields(raw)
		if len(fields) >= 3 {
			trashBytes, trashFiles, trashAt = fields[0], fields[1], fields[2]
		}
	}
	lock := ctx.readLock()
	lockHeld := "false"
	if lock.Held {
		lockHeld = "true"
	}
	age := "0"
	if lock.Held && lock.Since != "0" {
		if since, err := strconv.ParseInt(lock.Since, 10, 64); err == nil {
			age = strconv.FormatInt(time.Now().Unix()-since, 10)
		}
	}
	flockOK := commandExists("flock")
	shaOK := commandExists("sha256sum")
	caps := ""
	for _, line := range capabilityValues(ctx) {
		if strings.HasPrefix(line, "CAPABILITIES=") {
			caps = strings.TrimPrefix(line, "CAPABILITIES=")
		}
	}

	lines := []string{
		"VERSION=" + version,
		"RUNNING=" + strconv.FormatBool(running),
		"SHARE_ROOT=" + ctx.ShareRoot,
		"REPOSITORY_ID=" + id,
		"REPOSITORY_READY=" + strconv.FormatBool(id != ""),
		"RETENTION_DAYS=" + ctx.Config["RETENTION_DAYS"],
		"TRASH_MAX_MB=" + ctx.Config["TRASH_MAX_MB"],
		"TRASH_MIN_KEEP_DAYS=" + ctx.Config["TRASH_MIN_KEEP_DAYS"],
		"TRASH_USED_BYTES=" + trashBytes,
		"TRASH_FILES=" + trashFiles,
		"TRASH_USAGE_AT=" + trashAt,
		"TOMBSTONE_TTL_DAYS=" + ctx.Config["TOMBSTONE_TTL_DAYS"],
		"SYNC_LOCK_MAX_AGE_MINUTES=0",
		"SYNC_LOCK_HELD=" + lockHeld,
		"SYNC_LOCK_AGE_SECONDS=" + age,
		"SYNC_LOCK_OWNER_PID=" + lock.PID,
		"SYNC_LOCK_OWNER_ID=" + lock.ID,
		"SYNC_LOCK_OWNER_HOST=" + lock.Host,
		"SYNC_LOCK_STARTED_AT=" + lock.Since,
		"SYNC_LOCK_PHASE=" + lock.Phase,
		"SYNC_LOCK_PROGRESS_DONE=" + lock.Done,
		"SYNC_LOCK_PROGRESS_TOTAL=" + lock.Total,
		"CHECK_INTERVAL_MINUTES=" + ctx.Config["CHECK_INTERVAL_MINUTES"],
		"PRUNE_MAX_FILES_PER_PASS=" + ctx.Config["PRUNE_MAX_FILES_PER_PASS"],
		"JOURNAL_MAX_BYTES=" + ctx.Config["JOURNAL_MAX_BYTES"],
		"JOURNAL_MAX_LINES=" + ctx.Config["JOURNAL_MAX_LINES"],
		"LOG_MAX_BYTES=" + ctx.Config["LOG_MAX_BYTES"],
		"LOG_RETENTION_DAYS=" + ctx.Config["LOG_RETENTION_DAYS"],
		"EVENTS_MAX_BYTES=" + ctx.Config["EVENTS_MAX_BYTES"],
		"STATE_TEMP_MAX_AGE_HOURS=" + ctx.Config["STATE_TEMP_MAX_AGE_HOURS"],
		"CAPABILITIES=" + caps,
	}
	for _, line := range capabilityValues(ctx) {
		if !strings.HasPrefix(line, "CAPABILITIES=") {
			lines = append(lines, line)
		}
	}
	lines = append(lines,
		"JOURNAL_READY="+strconv.FormatBool(fileExists(p.Journal)),
		"STATE_DIR="+p.Dir,
	)
	if flockOK {
		lines = append(lines, "SYNC_LOCK_FILE="+p.Lock, "SYNC_LOCK_OWNER_FILE="+p.Owner)
	} else {
		lines = append(lines, "SYNC_LOCK_FILE=", "SYNC_LOCK_AVAILABLE=false")
	}
	lines = append(lines,
		"SYNC_LOCK_AVAILABLE="+strconv.FormatBool(flockOK),
		"PATH_RECONCILIATION_AVAILABLE="+strconv.FormatBool(shaOK && flockOK),
		"BROWSE_AVAILABLE="+strconv.FormatBool(flockOK),
	)
	for _, line := range lines {
		fmt.Println(line)
	}
	return nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func cmdJournalStatus(ctx *Context) error {
	p := ctx.paths()
	revision := readString(p.Revision)
	if revision == "" {
		revision = "0"
	}
	fmt.Println("JOURNAL_FILE=" + p.Journal)
	fmt.Println("MANIFEST_FILE=" + p.Manifest)
	fmt.Printf("JOURNAL_BYTES=%d\n", fileSize(p.Journal))
	fmt.Printf("MANIFEST_BYTES=%d\n", fileSize(p.Manifest))
	fmt.Println("JOURNAL_MAX_BYTES=" + ctx.Config["JOURNAL_MAX_BYTES"])
	fmt.Println("MANIFEST_REVISION=" + revision)
	return nil
}

func humanSize(ctx *Context, path string) string {
	out := runTrim("du", "-sh", path)
	fields := strings.Fields(out)
	if len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func cmdStatus(ctx *Context) error {
	p := ctx.paths()
	fmt.Printf("sync-daemon-server v%s\n", version)
	fmt.Printf("%-18s %s\n", "Script:", ctx.ScriptPath)
	fmt.Printf("%-18s %s\n", "Config:", ctx.Options.ConfigPath)
	share := ctx.ShareRoot
	if share == "" {
		share = "<non caricata>"
	}
	fmt.Printf("%-18s %s\n", "Cartella NASBox:", share)
	running := "FERMO"
	if raw := readString(p.DaemonPID); raw != "" {
		cmdline, err := os.ReadFile(filepath.Join("/proc", raw, "cmdline"))
		if err == nil {
			text := strings.ReplaceAll(string(cmdline), "\x00", " ")
			if strings.Contains(text, ctx.ScriptPath) && strings.Contains(text, "--run-foreground") {
				running = fmt.Sprintf("IN ESECUZIONE (PID %s, intervallo %s min)", raw, ctx.Config["CHECK_INTERVAL_MINUTES"])
			}
		}
	}
	fmt.Printf("%-18s %s\n", "Demone:", running)
	last := "mai"
	if raw := readString(p.LastRun); raw != "" {
		if ts, err := strconv.ParseInt(raw, 10, 64); err == nil {
			last = time.Unix(ts, 0).Format("2006-01-02 15:04:05")
		} else {
			last = raw
		}
	}
	fmt.Printf("%-18s %s\n", "Ultimo pass:", last)
	fmt.Printf("%-18s %s giorni\n", "Retention:", ctx.Config["RETENTION_DAYS"])
	if ttl := ctx.Config["TOMBSTONE_TTL_DAYS"]; ttl != "" && ttl != "0" {
		fmt.Printf("%-18s %s giorni\n", "Tombstone TTL:", ttl)
	}
	trash := filepath.Join(ctx.ShareRoot, ".sync-trash")
	size := humanSize(ctx, trash)
	if size == "" {
		fmt.Printf("%-18s %s\n", "Storico (spazio):", "(nessuno storico)")
	} else {
		fmt.Printf("%-18s %s\n", "Storico (spazio):", size)
	}
	fmt.Printf("%-18s %d byte\n", "Journal (spazio):", fileSize(p.Journal))
	fmt.Printf("%-18s %d byte\n", "Manifest (spazio):", fileSize(p.Manifest))
	fmt.Printf("%-18s %d byte\n", "Log server (spazio):", fileSize(p.ServerLog))
	fmt.Printf("%-18s %d byte\n", "Eventi (spazio):", fileSize(p.Events))
	lock := ctx.readLock()
	if !lock.Held {
		fmt.Println("Lock trasferimenti: libero")
	} else {
		fmt.Printf("Lock trasferimenti: occupato (host %s, device %s, PID %s, età %s secondi, fase %s %s/%s)\n",
			orDash(lock.Host), orDash(lock.ID), orDash(lock.PID), orDash(lock.Age), orDash(lock.Phase), lock.Done, lock.Total)
	}
	return nil
}

func orDash(value string) string {
	if value == "" {
		return "sconosciuto"
	}
	return value
}

func cmdDiagnostics(ctx *Context) error {
	p := ctx.paths()
	trash := filepath.Join(ctx.ShareRoot, ".sync-trash")
	count := 0
	_ = filepath.Walk(trash, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			count++
		}
		return nil
	})
	kb := runTrim("du", "-sk", trash)
	trashBytes := int64(0)
	if n, err := strconv.ParseInt(strings.Fields(kb)[0], 10, 64); err == nil {
		trashBytes = n * 1024
	}
	df := dfFields(ctx.ShareRoot)
	var total, used, avail int64
	if len(df) >= 5 {
		total, _ = strconv.ParseInt(df[1], 10, 64)
		used, _ = strconv.ParseInt(df[2], 10, 64)
		avail, _ = strconv.ParseInt(df[3], 10, 64)
		total *= 1024
		used *= 1024
		avail *= 1024
	}
	last := readString(p.LastRun)
	if last == "" {
		last = "0"
	}
	fmt.Println("VERSION=" + version)
	fmt.Printf("TRASH_FILE_COUNT=%d\n", count)
	fmt.Printf("TRASH_DISK_BYTES=%d\n", trashBytes)
	fmt.Printf("SHARE_TOTAL_BYTES=%d\n", total)
	fmt.Printf("SHARE_USED_BYTES=%d\n", used)
	fmt.Printf("SHARE_AVAILABLE_BYTES=%d\n", avail)
	fmt.Println("LAST_RUN_EPOCH=" + last)
	fmt.Printf("JOURNAL_BYTES=%d\n", fileSize(p.Journal))
	fmt.Printf("MANIFEST_BYTES=%d\n", fileSize(p.Manifest))
	fmt.Printf("SERVER_LOG_BYTES=%d\n", fileSize(p.ServerLog))
	fmt.Printf("EVENTS_BYTES=%d\n", fileSize(p.Events))
	fmt.Println("JOURNAL_MAX_BYTES=" + ctx.Config["JOURNAL_MAX_BYTES"])
	fmt.Println("JOURNAL_MAX_LINES=" + ctx.Config["JOURNAL_MAX_LINES"])
	fmt.Println("LOG_MAX_BYTES=" + ctx.Config["LOG_MAX_BYTES"])
	fmt.Println("EVENTS_MAX_BYTES=" + ctx.Config["EVENTS_MAX_BYTES"])
	fmt.Println("STATE_WRITABLE=" + strconv.FormatBool(isWritable(p.Dir)))
	return nil
}

func isWritable(dir string) bool {
	file, err := os.CreateTemp(dir, ".nasbox-write-test-*")
	if err != nil {
		return false
	}
	name := file.Name()
	file.Close()
	_ = os.Remove(name)
	return true
}

func cmdDevices(ctx *Context) error {
	p := ctx.paths()
	type device struct {
		last string
		ops  int
	}
	devices := map[string]*device{}
	journal, err := readLines(p.Journal)
	if err != nil {
		return err
	}
	for _, line := range journal {
		fields := strings.Split(line, "\t")
		if len(fields) < 4 || fields[0] != "BEGIN" {
			continue
		}
		entry, ok := devices[fields[3]]
		if !ok {
			entry = &device{last: fields[2]}
			devices[fields[3]] = entry
		}
		if fields[2] > entry.last {
			entry.last = fields[2]
		}
		entry.ops++
	}
	order := make([]string, 0, len(devices))
	for id := range devices {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool {
		a, _ := strconv.ParseInt(devices[order[i]].last, 10, 64)
		b, _ := strconv.ParseInt(devices[order[j]].last, 10, 64)
		return a > b
	})
	fmt.Println("NASBOX_DEVICES_V1")
	for _, id := range order {
		fmt.Printf("%s|%s|%d\n", id, devices[id].last, devices[id].ops)
	}
	if data, err := os.ReadFile(p.Owner); err == nil {
		fields := strings.Split(strings.TrimSpace(string(data)), "|")
		if len(fields) > 2 && fields[0] != "" && regexp.MustCompile(`^[0-9]+$`).MatchString(fields[2]) {
			seen, _ := strconv.ParseInt(fields[2], 10, 64)
			if time.Now().Unix()-seen <= 300 {
				host := "-"
				if len(fields) > 1 && fields[1] != "" {
					host = fields[1]
				}
				fmt.Printf("%s|%s|%s|ACTIVE\n", fields[0], host, fields[2])
			}
		}
	}
	return nil
}

func cmdManifestExport(ctx *Context, args []string) error {
	delta := len(args) > 0 && args[0] != ""
	since := int64(0)
	if delta {
		var err error
		since, err = strconv.ParseInt(args[0], 10, 64)
		if err != nil || since < 0 || since > 999999999999999999 {
			fmt.Fprintln(os.Stderr, "manifest export delta: since_revision non valido")
			return exitCode(75)
		}
	}
	view, err := ctx.exportManifest(since, delta)
	if err != nil {
		return err
	}
	if !delta {
		fmt.Println("NASBOX_MANIFEST_V1")
		for _, row := range view.Rows {
			fmt.Println(row)
		}
		return nil
	}
	if since > view.Current {
		fmt.Fprintln(os.Stderr, "manifest export delta: since_revision successiva alla revisione corrente")
		return exitCode(75)
	}
	fmt.Println("NASBOX_MANIFEST_DELTA_V1")
	fmt.Printf("BASE_REVISION=%d\n", since)
	fmt.Printf("CURRENT_REVISION=%d\n", view.Current)
	if since == view.Current {
		fmt.Printf("CHANGED=0\nREMOVED=\n")
		return nil
	}
	if view.FallbackFull {
		fmt.Println("FALLBACK_FULL=1")
	}
	fmt.Printf("CHANGED=%d\n", len(view.Rows))
	for _, row := range view.Rows {
		fmt.Println(row)
	}
	fmt.Println("REMOVED=")
	return nil
}

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func cmdManifestGet(ctx *Context, args []string) error {
	if len(args) == 0 || args[0] == "" || strings.HasPrefix(args[0], "/") {
		fmt.Fprintln(os.Stderr, "MANIFEST_INVALID")
		return exitCode(1)
	}
	for _, component := range strings.Split(args[0], "/") {
		if component == ".." {
			fmt.Fprintln(os.Stderr, "MANIFEST_INVALID")
			return exitCode(1)
		}
	}
	row, ok, err := ctx.lookupManifest(args[0])
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("MANIFEST_MISS")
		return nil
	}
	fmt.Printf("MANIFEST_HIT\t%s\n", row)
	return nil
}
