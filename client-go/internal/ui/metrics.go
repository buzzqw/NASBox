package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
	if c.nasLabel != nil {
		c.nasLabel.SetText(fmt.Sprintf(tr("ui.nas.config_short", nil), valueOrDash(c.cfg.String("nas_lan")),
			tr("settings.info_version_label", nil), metricText(values["VERSION"])))
	}
	available := humanBytes(parseMetricInt(values["DISK_AVAILABLE_BYTES"]))
	if c.diskValue != nil {
		c.diskValue.SetText(fmt.Sprintf("%s%% %s · %s %s", metricText(values["DISK_USAGE_PERCENT"]),
			tr("ui.nas.used", nil), available, tr("ui.nas.free", nil)))
	}
	if c.diskBar != nil {
		c.diskBar.SetValue(parseMetricFloat(values["DISK_USAGE_PERCENT"]) / 100)
	}
	if c.loadValue != nil {
		c.loadValue.SetText(fmt.Sprintf("1m %s · 5m %s · 15m %s",
			metricText(values["LOAD_1"]), metricText(values["LOAD_5"]), metricText(values["LOAD_15"])))
	}
	if c.memoryValue != nil {
		c.memoryValue.SetText(fmt.Sprintf("%s %s", humanBytes(parseMetricInt(values["MEM_AVAILABLE_BYTES"])),
			tr("ui.nas.available", nil)))
	}
	if c.lockValue != nil {
		phase := metricText(values["NASBOX_LOCK_PHASE"])
		c.lockValue.SetText(fmt.Sprintf("%s · %s", metricText(values["NASBOX_LOCK_HELD"]), phase))
	}
	if c.queueValue != nil {
		c.queueValue.SetText(metricText(values["NASBOX_QUEUE_COUNT"]))
	}
	if c.nasUpdatedLabel != nil {
		c.nasUpdatedLabel.SetText(tr("ui.nas.updated", map[string]any{"time": time.Now().Format("15:04:05")}))
	}
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
