package metrics

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func payload(total, idle, iowait, read, write int64) string {
	lines := []string{
		Protocol,
		"COLLECTED_AT_EPOCH=1700000000",
		"UPTIME_SECONDS=1000",
		"LOAD_1=0.50",
		"LOAD_5=0.40",
		"LOAD_15=0.30",
		"CPU_TOTAL_TICKS=" + itoa(total),
		"CPU_IDLE_TICKS=" + itoa(idle),
		"CPU_IOWAIT_TICKS=" + itoa(iowait),
		"MEM_TOTAL_BYTES=1000",
		"MEM_AVAILABLE_BYTES=400",
		"SWAP_TOTAL_BYTES=0",
		"SWAP_FREE_BYTES=0",
		"DISK_TOTAL_BYTES=10000",
		"DISK_USED_BYTES=6000",
		"DISK_AVAILABLE_BYTES=4000",
		"DISK_USAGE_PERCENT=60",
		"DISK_READ_BYTES=" + itoa(read),
		"DISK_WRITE_BYTES=" + itoa(write),
		"NET_RX_BYTES=4000",
		"NET_TX_BYTES=5000",
		"NET_RX_PACKETS=40",
		"NET_TX_PACKETS=50",
		"NET_INTERFACE_COUNT=2",
		"",
	}
	return strings.Join(lines, "\n")
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func TestParseOutputValidatesExtendedMetrics(t *testing.T) {
	sample, err := ParseOutput(payload(100, 40, 5, 2000, 3000))
	if err != nil {
		t.Fatal(err)
	}
	if sample.CPUTotalTicks != 100 || sample.DiskWriteBytes != 3000 || sample.NetInterfaceCount != 2 {
		t.Fatalf("unexpected sample: %+v", sample)
	}
}

func TestParseOutputRejectsMissingCounters(t *testing.T) {
	broken := strings.Replace(payload(100, 40, 5, 2000, 3000), "CPU_TOTAL_TICKS=100\n", "", 1)
	if _, err := ParseOutput(broken); !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v, want ErrProtocol", err)
	}
	if _, err := ParseOutput("hello world"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v, want ErrProtocol", err)
	}
}

func TestParseOutputRejectsInvalidValues(t *testing.T) {
	cases := []string{
		strings.Replace(payload(100, 40, 5, 2000, 3000), "DISK_USAGE_PERCENT=60", "DISK_USAGE_PERCENT=160", 1),
		strings.Replace(payload(100, 40, 5, 2000, 3000), "NET_RX_BYTES=4000", "NET_RX_BYTES=-1", 1),
		payload(100, 40, 5, 2000, 3000) + "NASBOX_LOCK_HELD=maybe\n",
	}
	for index, input := range cases {
		if _, err := ParseOutput(input); !errors.Is(err, ErrProtocol) {
			t.Fatalf("case %d: err = %v, want ErrProtocol", index, err)
		}
	}
}

func TestCollectorDerivesCPUDiskAndNetworkRates(t *testing.T) {
	collector := Collector{}
	start := time.Unix(1000, 0)
	first, err := ParseOutput(payload(100, 40, 5, 2000, 3000))
	if err != nil {
		t.Fatal(err)
	}
	if derived := collector.Observe(first, start); derived.CPUUsagePercent != nil {
		t.Fatal("first sample must not have derived rates")
	}
	second, err := ParseOutput(payload(200, 80, 15, 4000, 5000))
	if err != nil {
		t.Fatal(err)
	}
	derived := collector.Observe(second, start.Add(2*time.Second))
	if derived.CPUUsagePercent == nil || *derived.CPUUsagePercent != 60 {
		t.Fatalf("cpu usage = %v, want 60", derived.CPUUsagePercent)
	}
	if derived.CPUIOWaitPercent == nil || *derived.CPUIOWaitPercent != 10 {
		t.Fatalf("cpu iowait = %v, want 10", derived.CPUIOWaitPercent)
	}
	if derived.DiskReadRate == nil || *derived.DiskReadRate != 1000 {
		t.Fatalf("disk read rate = %v, want 1000", derived.DiskReadRate)
	}
	if derived.NetTxRate == nil || *derived.NetTxRate != 0 {
		t.Fatalf("net tx rate = %v, want 0", derived.NetTxRate)
	}
}

func TestHealthWarningsAndUptime(t *testing.T) {
	usage := 90.0
	wait := 25.0
	sample := Sample{CPUUsagePercent: &usage, CPUIOWaitPercent: &wait,
		MemTotalBytes: 100, MemAvailableBytes: 5, DiskUsagePercent: 95}
	got := strings.Join(HealthWarnings(sample), ",")
	if got != "CPU,I/O,RAM,disco" {
		t.Fatalf("warnings = %q", got)
	}
	if FormatUptime(90061) != "1d 01h 01m 01s" {
		t.Fatalf("uptime = %q", FormatUptime(90061))
	}
}

func TestParseValuesMatchesOutput(t *testing.T) {
	sample, err := ParseOutput(payload(100, 40, 5, 2000, 3000))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"COLLECTED_AT_EPOCH": "1700000000", "UPTIME_SECONDS": "1000",
		"LOAD_1": "0.50", "LOAD_5": "0.40", "LOAD_15": "0.30",
		"CPU_TOTAL_TICKS": "100", "CPU_IDLE_TICKS": "40", "CPU_IOWAIT_TICKS": "5",
		"MEM_TOTAL_BYTES": "1000", "MEM_AVAILABLE_BYTES": "400",
		"SWAP_TOTAL_BYTES": "0", "SWAP_FREE_BYTES": "0",
		"DISK_TOTAL_BYTES": "10000", "DISK_USED_BYTES": "6000",
		"DISK_AVAILABLE_BYTES": "4000", "DISK_USAGE_PERCENT": "60",
		"DISK_READ_BYTES": "2000", "DISK_WRITE_BYTES": "3000",
		"NET_RX_BYTES": "4000", "NET_TX_BYTES": "5000",
		"NET_RX_PACKETS": "40", "NET_TX_PACKETS": "50", "NET_INTERFACE_COUNT": "2",
	}
	fromMap, err := ParseValues(values)
	if err != nil {
		t.Fatal(err)
	}
	if fromMap.Load1 != sample.Load1 || fromMap.UptimeSeconds != sample.UptimeSeconds {
		t.Fatalf("map sample = %+v, output sample = %+v", fromMap, sample)
	}
}
