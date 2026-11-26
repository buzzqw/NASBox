package serverd

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var internalNames = map[string]bool{
	".sync-trash": true, ".sync-partial": true, ".nasbox-staging": true,
	".nasbox-root": true, "@eaDir": true,
}

func validRelative(path string) bool {
	if path == "" || filepath.IsAbs(path) {
		return false
	}
	for _, component := range strings.Split(path, "/") {
		if component == ".." || internalNames[component] {
			return false
		}
	}
	return true
}

func cmdBrowseList(ctx *Context, args []string) error {
	relative := ""
	if len(args) > 0 {
		relative = args[0]
	}
	if relative != "" && !validRelative(relative) {
		fmt.Fprintln(os.Stderr, "browse-list: percorso non valido")
		return exitCode(1)
	}
	target := ctx.ShareRoot
	if relative != "" {
		target = filepath.Join(target, relative)
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintln(os.Stderr, "browse-list: cartella non trovata")
		return exitCode(1)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	type item struct{ name, kind, size, mtime string }
	items := make([]item, 0, len(entries))
	for _, entry := range entries {
		if internalNames[entry.Name()] {
			continue
		}
		path := filepath.Join(target, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		entry := item{name: entry.Name(), kind: "OTHER", size: "0", mtime: "0"}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
		case info.IsDir():
			entry.kind = "DIR"
			entry.mtime = strconv.FormatInt(info.ModTime().Unix(), 10)
		case info.Mode().IsRegular():
			entry.kind = "FILE"
			entry.size = strconv.FormatInt(info.Size(), 10)
			entry.mtime = strconv.FormatInt(info.ModTime().Unix(), 10)
		}
		items = append(items, entry)
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	fmt.Fprintf(out, "BROWSE_LIST_V1\x00%d\x00", len(items))
	for _, entry := range items {
		fmt.Fprintf(out, "%s\x00%s\x00%s\x00%s\x00", entry.name, entry.kind, entry.size, entry.mtime)
	}
	return nil
}

var historySuffix = regexp.MustCompile(`-([0-9]{4}-[0-9]{2}-[0-9]{2}--[0-9]{2}-[0-9]{2}-[0-9]{2})(-([0-9]{6})Z)?$`)

func cmdHistoryList(ctx *Context) error {
	trash := filepath.Join(ctx.ShareRoot, ".sync-trash")
	type item struct{ original, suffix, rel, size string }
	items := []item{}
	_ = filepath.Walk(trash, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.Mode().IsRegular() {
			return nil
		}
		match := historySuffix.FindStringSubmatch(info.Name())
		if match == nil {
			return nil
		}
		suffix := match[1] + match[2]
		rel, err := filepath.Rel(trash, path)
		if err != nil {
			return nil
		}
		original := strings.TrimSuffix(rel, "-"+suffix)
		items = append(items, item{original: original, suffix: suffix, rel: rel, size: strconv.FormatInt(info.Size(), 10)})
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].rel < items[j].rel })
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	fmt.Fprint(out, "HISTORY_V1\x00")
	for _, entry := range items {
		fmt.Fprintf(out, "%s\x00%s\x00%s\x00%s\x00", entry.original, entry.suffix, entry.rel, entry.size)
	}
	return nil
}

func procFields(path string, lineIndex, fieldsNeeded int) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for index := 0; scanner.Scan(); index++ {
		if index != lineIndex {
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) >= fieldsNeeded {
			return fields
		}
		return nil
	}
	return nil
}

func floatString(value float64) string {
	return strconv.FormatFloat(math.Round(value), 'f', 0, 64)
}

func cmdMetrics(ctx *Context) error {
	for _, line := range ctx.metricsLines() {
		fmt.Println(line)
	}
	return nil
}

func (ctx *Context) metricsLines() []string {
	p := ctx.paths()
	uptime := "0"
	if fields := procFields("/proc/uptime", 0, 1); fields != nil {
		if value, err := strconv.ParseFloat(fields[0], 64); err == nil {
			uptime = floatString(value)
		}
	}
	load := []string{"0", "0", "0"}
	if fields := procFields("/proc/loadavg", 0, 3); fields != nil {
		copy(load, fields[:3])
	}
	cpuTotal, cpuIdle, cpuIOWait := "0", "0", "0"
	if fields := procFields("/proc/stat", 0, 10); fields != nil {
		var total, idle, iowait int64
		for i := 1; i <= 8 && i < len(fields); i++ {
			value, _ := strconv.ParseInt(fields[i], 10, 64)
			total += value
		}
		idle, _ = strconv.ParseInt(fields[4], 10, 64)
		iowait, _ = strconv.ParseInt(fields[5], 10, 64)
		idle += iowait
		cpuTotal, cpuIdle, cpuIOWait = strconv.FormatInt(total, 10), strconv.FormatInt(idle, 10), strconv.FormatInt(iowait, 10)
	}
	memTotal, memAvail, swapTotal, swapFree := "0", "0", "0", "0"
	if file, err := os.Open("/proc/meminfo"); err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 2 {
				continue
			}
			value, _ := strconv.ParseInt(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				memTotal = strconv.FormatInt(value*1024, 10)
			case "MemAvailable:":
				memAvail = strconv.FormatInt(value*1024, 10)
			case "SwapTotal:":
				swapTotal = strconv.FormatInt(value*1024, 10)
			case "SwapFree:":
				swapFree = strconv.FormatInt(value*1024, 10)
			}
		}
		file.Close()
	}
	diskFields := dfFields(ctx.ShareRoot)
	diskTotal, diskUsed, diskAvail, diskPercent := "0", "0", "0", "0"
	if len(diskFields) >= 5 {
		for i, target := range []*string{&diskTotal, &diskUsed, &diskAvail} {
			value, _ := strconv.ParseInt(diskFields[i+1], 10, 64)
			*target = strconv.FormatInt(value*1024, 10)
		}
		diskPercent = strings.TrimSuffix(diskFields[4], "%")
	}
	var readSectors, writeSectors int64
	if file, err := os.Open("/proc/diskstats"); err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 10 {
				continue
			}
			name := fields[2]
			ok := false
			for _, prefix := range []string{"sd", "hd", "vd", "xvd", "nvme", "mmcblk", "md", "dm"} {
				if strings.HasPrefix(name, prefix) {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
			read, _ := strconv.ParseInt(fields[5], 10, 64)
			written, _ := strconv.ParseInt(fields[9], 10, 64)
			readSectors += read
			writeSectors += written
		}
		file.Close()
	}
	netRX, netTX, netRXP, netTXP := "0", "0", "0", "0"
	interfaces := 0
	if file, err := os.Open("/proc/net/dev"); err == nil {
		scanner := bufio.NewScanner(file)
		var totalRX, totalTX, totalRXP, totalTXP int64
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.SplitN(line, ":", 2)
			if len(parts) < 2 {
				continue
			}
			name := strings.TrimSpace(parts[0])
			if name == "lo" {
				continue
			}
			interfaces++
			fields := strings.Fields(parts[1])
			if len(fields) < 11 {
				continue
			}
			rx, _ := strconv.ParseInt(fields[0], 10, 64)
			rxp, _ := strconv.ParseInt(fields[1], 10, 64)
			tx, _ := strconv.ParseInt(fields[8], 10, 64)
			txp, _ := strconv.ParseInt(fields[9], 10, 64)
			totalRX += rx
			totalRXP += rxp
			totalTX += tx
			totalTXP += txp
		}
		netRX, netTX = strconv.FormatInt(totalRX, 10), strconv.FormatInt(totalTX, 10)
		netRXP, netTXP = strconv.FormatInt(totalRXP, 10), strconv.FormatInt(totalTXP, 10)
		file.Close()
	}
	owner := lockState{}
	if fileExists(p.Lock) {
		owner = ctx.readLock()
	}
	queue := 0
	if entries, err := os.ReadDir(p.QueueDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ticket") {
				queue++
			}
		}
	}
	staging := 0
	if entries, err := os.ReadDir(filepath.Join(ctx.ShareRoot, ".nasbox-staging")); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				staging++
			}
		}
	}
	lines := []string{
		"NASBOX_METRICS_V1",
		"COLLECTED_AT_EPOCH=" + strconv.FormatInt(time.Now().Unix(), 10),
		"UPTIME_SECONDS=" + uptime,
		"LOAD_1=" + load[0], "LOAD_5=" + load[1], "LOAD_15=" + load[2],
		"CPU_TOTAL_TICKS=" + cpuTotal,
		"CPU_IDLE_TICKS=" + cpuIdle,
		"CPU_IOWAIT_TICKS=" + cpuIOWait,
		"MEM_TOTAL_BYTES=" + memTotal,
		"MEM_AVAILABLE_BYTES=" + memAvail,
		"SWAP_TOTAL_BYTES=" + swapTotal,
		"SWAP_FREE_BYTES=" + swapFree,
		"DISK_TOTAL_BYTES=" + diskTotal,
		"DISK_USED_BYTES=" + diskUsed,
		"DISK_AVAILABLE_BYTES=" + diskAvail,
		"DISK_USAGE_PERCENT=" + diskPercent,
		"DISK_READ_BYTES=" + strconv.FormatInt(readSectors*512, 10),
		"DISK_WRITE_BYTES=" + strconv.FormatInt(writeSectors*512, 10),
		"NET_RX_BYTES=" + netRX,
		"NET_TX_BYTES=" + netTX,
		"NET_RX_PACKETS=" + netRXP,
		"NET_TX_PACKETS=" + netTXP,
		fmt.Sprintf("NET_INTERFACE_COUNT=%d", interfaces),
		"NASBOX_LOCK_HELD=" + strconv.FormatBool(owner.Held),
		"NASBOX_LOCK_OWNER=" + owner.ID,
		"NASBOX_LOCK_HOST=" + owner.Host,
		"NASBOX_LOCK_PHASE=" + owner.Phase,
		"NASBOX_LOCK_STARTED_AT=" + owner.Since,
		"NASBOX_LOCK_PROGRESS_DONE=" + owner.Done,
		"NASBOX_LOCK_PROGRESS_TOTAL=" + owner.Total,
		fmt.Sprintf("NASBOX_QUEUE_COUNT=%d", queue),
		fmt.Sprintf("NASBOX_STAGING_COUNT=%d", staging),
		fmt.Sprintf("JOURNAL_BYTES=%d", fileSize(p.Journal)),
		fmt.Sprintf("MANIFEST_BYTES=%d", fileSize(p.Manifest)),
	}
	return lines
}
