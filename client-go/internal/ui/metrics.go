package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"nasbox-go/internal/metrics"
)

// deviceEntry is the UI representation of one journal row returned by the NAS.
// The raw protocol stays in the remote package; this view keeps the stable ID
// separate from the user-assigned display name and IP.
type deviceEntry struct {
	ID         string
	Name       string
	IP         string
	LastSeen   time.Time
	Operations string
	Active     bool
}

func parseDeviceEntries(raw []string) []deviceEntry {
	entries := make([]deviceEntry, 0, len(raw))
	for _, line := range raw {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) < 3 || parts[0] == "" {
			continue
		}
		entry := deviceEntry{ID: parts[0], Operations: parts[2]}
		if seconds, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
			entry.LastSeen = time.Unix(seconds, 0)
		}
		entry.Active = len(parts) > 3 && parts[3] == "ACTIVE"
		entries = append(entries, entry)
	}
	return entries
}

func (c *clientApp) deviceDetails(entry deviceEntry) deviceEntry {
	labels, _ := c.cfg.Get("device_labels").(map[string]any)
	if label, ok := labels[entry.ID].(map[string]any); ok {
		if name := strings.TrimSpace(fmt.Sprint(label["name"])); name != "" && name != "<nil>" {
			entry.Name = name
		}
		if ip := strings.TrimSpace(fmt.Sprint(label["ip"])); ip != "" && ip != "<nil>" {
			entry.IP = ip
		}
	}
	if entry.Name == "" {
		entry.Name = fmt.Sprintf("%s %s", tr("ui.device.default_name", nil), entry.ID)
	}
	return entry
}

func (c *clientApp) deviceName(entry deviceEntry) string {
	return c.deviceDetails(entry).Name
}

func (c *clientApp) deviceSubtitle(entry deviceEntry) string {
	entry = c.deviceDetails(entry)
	parts := []string{}
	if entry.IP != "" {
		parts = append(parts, entry.IP)
	} else {
		parts = append(parts, "IP —")
	}
	if !entry.LastSeen.IsZero() {
		parts = append(parts, entry.LastSeen.Format("02/01 15:04"))
	}
	parts = append(parts, fmt.Sprintf("%s %s", entry.Operations, tr("ui.device.operations", nil)))
	if entry.Active {
		parts = append(parts, tr("devices.active", nil))
	}
	return strings.Join(parts, " · ")
}

func parseMetricInt(value string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed
}

func parseMetricFloat(value string) float64 {
	parsed, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return parsed
}

func metricText(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

func (c *clientApp) updateMetricsUI(values map[string]string) {
	sample, err := metrics.ParseValues(values)
	if err != nil {
		if c.healthValue != nil {
			c.healthValue.SetText(tr("metrics.status_failed", map[string]any{"detail": err.Error()}))
		}
		return
	}
	sample = c.metricsCollector.Observe(sample, time.Now())
	if c.nasLabel != nil {
		c.nasLabel.SetText(fmt.Sprintf(tr("ui.nas.config_short", nil), valueOrDash(c.cfg.String("nas_lan")),
			tr("settings.info_version_label", nil), metricText(values["VERSION"])))
	}
	diskText := tr("metrics.disk_value", map[string]any{
		"available": humanBytes(sample.DiskAvailableBytes),
		"total":     humanBytes(sample.DiskTotalBytes),
		"percent":   fmt.Sprintf("%.0f", sample.DiskUsagePercent),
	})
	if c.diskValue != nil {
		c.diskValue.SetText(diskText)
	}
	if c.diskBar != nil {
		c.diskBar.SetValue(sample.DiskUsagePercent / 100)
	}
	if c.loadValue != nil {
		c.loadValue.SetText(fmt.Sprintf("%.2f / %.2f / %.2f", sample.Load1, sample.Load5, sample.Load15))
	}
	if c.cpuValue != nil {
		if sample.CPUUsagePercent == nil {
			c.cpuValue.SetText(tr("metrics.first_sample", nil))
		} else {
			c.cpuValue.SetText(tr("metrics.cpu_value", map[string]any{
				"usage":  fmt.Sprintf("%.0f", *sample.CPUUsagePercent),
				"iowait": fmt.Sprintf("%.0f", valueOrZero(sample.CPUIOWaitPercent)),
			}))
		}
	}
	if c.memoryValue != nil {
		if sample.MemTotalBytes > 0 {
			used := sample.MemTotalBytes - sample.MemAvailableBytes
			if used < 0 {
				used = 0
			}
			c.memoryValue.SetText(tr("metrics.memory_value", map[string]any{
				"used": humanBytes(used), "total": humanBytes(sample.MemTotalBytes),
				"percent": fmt.Sprintf("%.0f", float64(used)*100/float64(sample.MemTotalBytes)),
			}))
		} else {
			c.memoryValue.SetText(tr("metrics.not_available", nil))
		}
	}
	if c.swapValue != nil {
		if sample.SwapTotalBytes > 0 {
			used := sample.SwapTotalBytes - sample.SwapFreeBytes
			if used < 0 {
				used = 0
			}
			c.swapValue.SetText(tr("metrics.swap_value", map[string]any{
				"used": humanBytes(used), "total": humanBytes(sample.SwapTotalBytes),
			}))
		} else {
			c.swapValue.SetText(tr("metrics.swap_disabled", nil))
		}
	}
	if c.netValue != nil {
		c.netValue.SetText(fmt.Sprintf("↓ %s  ↑ %s", rateText(sample.NetRxRate, sample.NetRxBytes),
			rateText(sample.NetTxRate, sample.NetTxBytes)))
	}
	if c.ioValue != nil {
		c.ioValue.SetText(fmt.Sprintf("R %s  W %s", rateText(sample.DiskReadRate, sample.DiskReadBytes),
			rateText(sample.DiskWriteRate, sample.DiskWriteBytes)))
	}
	if c.uptimeValue != nil {
		c.uptimeValue.SetText(metrics.FormatUptime(sample.UptimeSeconds))
	}
	if c.lockValue != nil {
		c.lockValue.SetText(fmt.Sprintf("%t · %s", sample.NasboxLockHeld, metricText(sample.NasboxLockPhase)))
	}
	if c.queueValue != nil {
		c.queueValue.SetText(fmt.Sprint(sample.QueueCount))
	}
	if c.stagingValue != nil {
		c.stagingValue.SetText(fmt.Sprint(sample.StagingCount))
	}
	if c.metadataValue != nil {
		c.metadataValue.SetText(tr("metrics.metadata_value", map[string]any{
			"journal": humanBytes(sample.JournalBytes), "manifest": humanBytes(sample.ManifestBytes),
		}))
	}
	if c.activityValue != nil {
		if sample.NasboxLockHeld {
			progress := ""
			if sample.NasboxLockProgressTotal > 0 {
				progress = fmt.Sprintf(" %d/%d", sample.NasboxLockProgressDone, sample.NasboxLockProgressTotal)
			}
			owner := sample.NasboxLockOwner
			if owner == "" {
				owner = tr("metrics.unknown_value", nil)
			}
			phase := sample.NasboxLockPhase
			if phase == "" {
				phase = tr("metrics.unknown_value", nil)
			}
			c.activityValue.SetText(tr("metrics.activity_busy", map[string]any{
				"owner": owner, "phase": phase, "progress": progress,
			}))
		} else {
			c.activityValue.SetText(tr("metrics.activity_idle", nil))
		}
	}
	if c.healthValue != nil {
		warnings := metrics.HealthWarnings(sample)
		if len(warnings) > 0 {
			c.healthValue.SetText(tr("metrics.health_warning", map[string]any{"areas": strings.Join(warnings, ", ")}))
		} else {
			c.healthValue.SetText(tr("metrics.health_ok", nil))
		}
	}
	if c.nasUpdatedLabel != nil {
		c.nasUpdatedLabel.SetText(tr("metrics.status_updated", map[string]any{
			"time": time.Now().Format("15:04:05"),
		}))
	}
}

func valueOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func rateText(rate *float64, total int64) string {
	if rate == nil {
		return humanBytes(total)
	}
	return fmt.Sprintf("%s/s", humanBytes(int64(*rate)))
}

func (c *clientApp) updateDevicesUI(raw []string) {
	c.deviceEntries = parseDeviceEntries(raw)
	for index := range c.deviceEntries {
		c.deviceEntries[index] = c.deviceDetails(c.deviceEntries[index])
	}
	if c.deviceList != nil {
		c.deviceList.Refresh()
	}
	if c.selectedDevice != "" {
		found := false
		for _, entry := range c.deviceEntries {
			if entry.ID == c.selectedDevice {
				found = true
				break
			}
		}
		if !found {
			c.selectedDevice = ""
		}
	}
	if c.selectedDeviceLabel != nil {
		if c.selectedDevice == "" {
			c.selectedDeviceLabel.SetText(tr("ui.device.none_selected", nil))
		} else {
			c.selectedDeviceLabel.SetText(tr("ui.device.selected", map[string]any{"id": c.selectedDevice}))
		}
	}
	if c.monitorDevicesLabel != nil {
		if len(c.deviceEntries) == 0 {
			c.monitorDevicesLabel.SetText(tr("devices.empty", nil))
			return
		}
		lines := make([]string, 0, len(c.deviceEntries))
		for _, entry := range c.deviceEntries {
			lines = append(lines, fmt.Sprintf("%s — %s", c.deviceName(entry), c.deviceSubtitle(entry)))
		}
		c.monitorDevicesLabel.SetText(strings.Join(lines, "\n"))
	}
}
