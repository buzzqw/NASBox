// Package metrics parses the NAS monitor payload and derives the client-side
// rates the UI shows. The NAS stays stateless: it exports counters, the PC
// computes CPU, disk and network rates from two samples.
//
// The protocol is the same NASBOX_METRICS_V1 document used by the Python
// client, including its optional NASBox activity fields (lock owner/phase,
// queue, staging, journal and manifest sizes).
package metrics

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Protocol is the first line of a valid server response.
const Protocol = "NASBOX_METRICS_V1"

var required = []string{
	"COLLECTED_AT_EPOCH", "UPTIME_SECONDS", "LOAD_1", "LOAD_5", "LOAD_15",
	"CPU_TOTAL_TICKS", "CPU_IDLE_TICKS", "CPU_IOWAIT_TICKS",
	"MEM_TOTAL_BYTES", "MEM_AVAILABLE_BYTES", "SWAP_TOTAL_BYTES", "SWAP_FREE_BYTES",
	"DISK_TOTAL_BYTES", "DISK_USED_BYTES", "DISK_AVAILABLE_BYTES", "DISK_USAGE_PERCENT",
	"DISK_READ_BYTES", "DISK_WRITE_BYTES",
	"NET_RX_BYTES", "NET_TX_BYTES", "NET_RX_PACKETS", "NET_TX_PACKETS",
	"NET_INTERFACE_COUNT",
}

var integerFields = map[string]bool{
	"COLLECTED_AT_EPOCH": true, "UPTIME_SECONDS": true,
	"MEM_TOTAL_BYTES": true, "MEM_AVAILABLE_BYTES": true,
	"SWAP_TOTAL_BYTES": true, "SWAP_FREE_BYTES": true,
	"DISK_TOTAL_BYTES": true, "DISK_USED_BYTES": true, "DISK_AVAILABLE_BYTES": true,
	"CPU_TOTAL_TICKS": true, "CPU_IDLE_TICKS": true, "CPU_IOWAIT_TICKS": true,
	"DISK_READ_BYTES": true, "DISK_WRITE_BYTES": true,
	"NET_RX_BYTES": true, "NET_TX_BYTES": true,
	"NET_RX_PACKETS": true, "NET_TX_PACKETS": true, "NET_INTERFACE_COUNT": true,
}

var optionalIntegers = []string{
	"NASBOX_LOCK_STARTED_AT", "NASBOX_LOCK_PROGRESS_DONE", "NASBOX_LOCK_PROGRESS_TOTAL",
	"NASBOX_QUEUE_COUNT", "JOURNAL_BYTES", "MANIFEST_BYTES", "NASBOX_STAGING_COUNT",
}

// ErrProtocol marks a malformed or incomplete NAS response.
var ErrProtocol = errors.New("metriche NAS non valide")

// Sample is one validated server snapshot. Derived fields stay nil until a
// second sample is available.
type Sample struct {
	CollectedAtEpoch int64
	UptimeSeconds    int64
	Load1            float64
	Load5            float64
	Load15           float64

	CPUTotalTicks  int64
	CPUIdleTicks   int64
	CPUIOWaitTicks int64

	MemTotalBytes     int64
	MemAvailableBytes int64
	SwapTotalBytes    int64
	SwapFreeBytes     int64

	DiskTotalBytes     int64
	DiskUsedBytes      int64
	DiskAvailableBytes int64
	DiskUsagePercent   float64
	DiskReadBytes      int64
	DiskWriteBytes     int64

	NetRxBytes        int64
	NetTxBytes        int64
	NetRxPackets      int64
	NetTxPackets      int64
	NetInterfaceCount int

	NasboxLockHeld          bool
	NasboxLockOwner         string
	NasboxLockHost          string
	NasboxLockPhase         string
	NasboxLockStartedAt     int64
	NasboxLockProgressDone  int64
	NasboxLockProgressTotal int64
	QueueCount              int64
	StagingCount            int64
	JournalBytes            int64
	ManifestBytes           int64

	CPUUsagePercent  *float64
	CPUIOWaitPercent *float64
	NetRxRate        *float64
	NetTxRate        *float64
	DiskReadRate     *float64
	DiskWriteRate    *float64
}

// ParseOutput validates a raw `--metrics` response.
func ParseOutput(output string) (Sample, error) {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != Protocol {
		return Sample{}, fmt.Errorf("%w: risposta NAS non riconosciuta", ErrProtocol)
	}
	values := map[string]string{}
	for _, line := range lines[1:] {
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" {
			continue
		}
		if _, exists := values[key]; !exists {
			values[key] = strings.TrimSpace(value)
		}
	}
	return ParseValues(values)
}

// ParseValues validates the KEY=VALUE body. The Go SSH client already parses
// the response into a map, so both callers share the same validation rules.
func ParseValues(values map[string]string) (Sample, error) {
	missing := make([]string, 0)
	for _, key := range required {
		if _, ok := values[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return Sample{}, fmt.Errorf("%w: metriche NAS mancanti: %s", ErrProtocol, strings.Join(missing, ", "))
	}
	integers := map[string]int64{}
	floats := map[string]float64{}
	for _, key := range required {
		raw := values[key]
		if integerFields[key] {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value < 0 {
				return Sample{}, fmt.Errorf("%w: valore non valido per %s", ErrProtocol, key)
			}
			integers[key] = value
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 {
			return Sample{}, fmt.Errorf("%w: valore non valido per %s", ErrProtocol, key)
		}
		floats[key] = value
	}
	if floats["DISK_USAGE_PERCENT"] > 100 {
		return Sample{}, fmt.Errorf("%w: percentuale disco non valida", ErrProtocol)
	}
	optional := map[string]int64{}
	for _, key := range optionalIntegers {
		raw, ok := values[key]
		if !ok {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			return Sample{}, fmt.Errorf("%w: valore opzionale non valido per %s", ErrProtocol, key)
		}
		optional[key] = value
	}
	lockHeld := false
	switch values["NASBOX_LOCK_HELD"] {
	case "", "false":
	case "true":
		lockHeld = true
	default:
		return Sample{}, fmt.Errorf("%w: stato lock NAS non valido", ErrProtocol)
	}
	return Sample{
		CollectedAtEpoch: integers["COLLECTED_AT_EPOCH"],
		UptimeSeconds:    integers["UPTIME_SECONDS"],
		Load1:            floats["LOAD_1"], Load5: floats["LOAD_5"], Load15: floats["LOAD_15"],
		CPUTotalTicks: integers["CPU_TOTAL_TICKS"], CPUIdleTicks: integers["CPU_IDLE_TICKS"],
		CPUIOWaitTicks: integers["CPU_IOWAIT_TICKS"],
		MemTotalBytes:  integers["MEM_TOTAL_BYTES"], MemAvailableBytes: integers["MEM_AVAILABLE_BYTES"],
		SwapTotalBytes: integers["SWAP_TOTAL_BYTES"], SwapFreeBytes: integers["SWAP_FREE_BYTES"],
		DiskTotalBytes: integers["DISK_TOTAL_BYTES"], DiskUsedBytes: integers["DISK_USED_BYTES"],
		DiskAvailableBytes: integers["DISK_AVAILABLE_BYTES"], DiskUsagePercent: floats["DISK_USAGE_PERCENT"],
		DiskReadBytes: integers["DISK_READ_BYTES"], DiskWriteBytes: integers["DISK_WRITE_BYTES"],
		NetRxBytes: integers["NET_RX_BYTES"], NetTxBytes: integers["NET_TX_BYTES"],
		NetRxPackets: integers["NET_RX_PACKETS"], NetTxPackets: integers["NET_TX_PACKETS"],
		NetInterfaceCount:       int(integers["NET_INTERFACE_COUNT"]),
		NasboxLockHeld:          lockHeld,
		NasboxLockOwner:         values["NASBOX_LOCK_OWNER"],
		NasboxLockHost:          values["NASBOX_LOCK_HOST"],
		NasboxLockPhase:         values["NASBOX_LOCK_PHASE"],
		NasboxLockStartedAt:     optional["NASBOX_LOCK_STARTED_AT"],
		NasboxLockProgressDone:  optional["NASBOX_LOCK_PROGRESS_DONE"],
		NasboxLockProgressTotal: optional["NASBOX_LOCK_PROGRESS_TOTAL"],
		QueueCount:              optional["NASBOX_QUEUE_COUNT"], StagingCount: optional["NASBOX_STAGING_COUNT"],
		JournalBytes: optional["JOURNAL_BYTES"], ManifestBytes: optional["MANIFEST_BYTES"],
	}, nil
}

// Collector remembers the previous sample and derives rates.
type Collector struct {
	previous   *Sample
	previousAt time.Time
}

// Reset forgets the previous sample (for example after a reconnect).
func (c *Collector) Reset() {
	c.previous = nil
	c.previousAt = time.Time{}
}

// Observe stores the sample and returns a copy with derived rates when a
// previous sample exists. Elapsed time is passed explicitly so tests do not
// depend on the wall clock.
func (c *Collector) Observe(sample Sample, at time.Time) Sample {
	previous := c.previous
	previousAt := c.previousAt
	c.previous = &sample
	c.previousAt = at
	if previous == nil {
		return sample
	}
	elapsed := at.Sub(previousAt).Seconds()
	if elapsed <= 0 {
		return sample
	}
	rate := func(current, old int64) *float64 {
		delta := current - old
		if delta < 0 {
			delta = 0
		}
		value := float64(delta) / elapsed
		return &value
	}
	sample.NetRxRate = rate(sample.NetRxBytes, previous.NetRxBytes)
	sample.NetTxRate = rate(sample.NetTxBytes, previous.NetTxBytes)
	sample.DiskReadRate = rate(sample.DiskReadBytes, previous.DiskReadBytes)
	sample.DiskWriteRate = rate(sample.DiskWriteBytes, previous.DiskWriteBytes)
	totalDelta := sample.CPUTotalTicks - previous.CPUTotalTicks
	if totalDelta > 0 {
		busy := (sample.CPUTotalTicks - sample.CPUIdleTicks) -
			(previous.CPUTotalTicks - previous.CPUIdleTicks)
		if busy < 0 {
			busy = 0
		}
		iowait := sample.CPUIOWaitTicks - previous.CPUIOWaitTicks
		if iowait < 0 {
			iowait = 0
		}
		usage := float64(busy) * 100 / float64(totalDelta)
		wait := float64(iowait) * 100 / float64(totalDelta)
		sample.CPUUsagePercent = &usage
		sample.CPUIOWaitPercent = &wait
	}
	return sample
}

// HealthWarnings lists the pressure areas above the Python client's
// thresholds: CPU >= 85%, I/O wait >= 20%, RAM < 10% free, disk >= 90%.
func HealthWarnings(sample Sample) []string {
	warnings := make([]string, 0, 4)
	if sample.CPUUsagePercent != nil && *sample.CPUUsagePercent >= 85 {
		warnings = append(warnings, "CPU")
	}
	if sample.CPUIOWaitPercent != nil && *sample.CPUIOWaitPercent >= 20 {
		warnings = append(warnings, "I/O")
	}
	if sample.MemTotalBytes > 0 && sample.MemAvailableBytes*100/sample.MemTotalBytes < 10 {
		warnings = append(warnings, "RAM")
	}
	if sample.DiskUsagePercent >= 90 {
		warnings = append(warnings, "disco")
	}
	return warnings
}

// FormatUptime renders seconds as "Nd HHh MMm SSs".
func FormatUptime(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	rest := seconds % 60
	return fmt.Sprintf("%dd %02dh %02dm %02ds", days, hours, minutes, rest)
}
