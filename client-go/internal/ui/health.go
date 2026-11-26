package ui

import (
	"fmt"
	"nasbox-go/internal/osx"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"nasbox-go/internal/repository"
	"nasbox-go/internal/transfer"
	"nasbox-go/internal/version"
)

// healthCheck is one line of "Controlla il sistema": what was checked, the
// outcome and, when something is wrong, what to do about it.
type healthCheck struct {
	Level  string // ok | warn | error
	Title  string
	Detail string
	Hint   string
}

// runHealthChecks performs the same checks as `nasbox doctor`, plus the
// environment checks that explain most real-world problems.
func (c *clientApp) runHealthChecks() []healthCheck {
	checks := []healthCheck{}
	add := func(level, title, detail, hint string) {
		checks = append(checks, healthCheck{level, title, detail, hint})
	}

	root := c.cfg.LocalRoot()
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		add("error", tr("ui.health.local_root", nil), root, tr("ui.health.local_root_hint", nil))
	} else {
		probe := filepath.Join(root, ".nasbox-write-probe")
		if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
			add("error", tr("ui.health.local_root", nil), err.Error(), tr("ui.health.local_root_hint", nil))
		} else {
			_ = os.Remove(probe)
			free := ""
			if available, _, ok := osx.FreeBytes(root); ok {
				freeBytes := int64(available)
				free = humanBytes(freeBytes)
				if freeBytes < 2<<30 {
					add("warn", tr("ui.health.local_space", nil), free, tr("ui.health.local_space_hint", nil))
				}
			}
			add("ok", tr("ui.health.local_root", nil), fmt.Sprintf("%s · %s %s", root, free, tr("ui.health.free", nil)), "")
		}
	}
	if err := repository.Validate(root, c.cfg.RepositoryID(), false); err != nil {
		add("error", tr("ui.health.repository", nil), err.Error(), tr("ui.health.repository_hint", nil))
	} else {
		add("ok", tr("ui.health.repository", nil), shortID(c.cfg.RepositoryID()), "")
	}

	values, err := c.client.PrintConfig()
	if err != nil {
		add("error", tr("ui.health.server", nil), err.Error(), tr("ui.health.server_hint", nil))
	} else {
		serverVersion := values["VERSION"]
		minimum := c.cfg.String("min_server_version")
		if minimum != "" && lessVersion(serverVersion, minimum) {
			add("error", tr("ui.health.server", nil), serverVersion, tr("ui.health.server_old_hint", map[string]any{"min": minimum}))
		} else {
			add("ok", tr("ui.health.server", nil), fmt.Sprintf("%s · %s", c.cfg.String("nas_lan"), serverVersion), "")
		}
		if strings.ToLower(values["RUNNING"]) != "true" {
			add("warn", tr("ui.health.daemon", nil), tr("ui.health.daemon_stopped", nil), tr("ui.health.daemon_hint", nil))
		} else {
			add("ok", tr("ui.health.daemon", nil), tr("ui.health.daemon_running", nil), "")
		}
		if used, err := strconv.ParseInt(values["TRASH_USED_BYTES"], 10, 64); err == nil {
			limit, _ := strconv.ParseInt(values["TRASH_MAX_MB"], 10, 64)
			detail := humanBytes(used)
			if limit > 0 {
				detail += " / " + humanBytes(limit*1048576)
				add("ok", tr("ui.health.trash", nil), detail, "")
			} else {
				add("warn", tr("ui.health.trash", nil), detail+" · "+tr("ui.health.trash_no_quota", nil),
					tr("ui.health.trash_hint", nil))
			}
		}
		if strings.ToLower(values["JOURNAL_READY"]) != "true" {
			add("error", tr("ui.health.journal", nil), values["JOURNAL_READY"], tr("ui.health.journal_hint", nil))
		}
	}

	localRsync := transfer.LocalRsyncVersion()
	switch {
	case localRsync == "":
		add("error", tr("ui.health.rsync_local", nil), "—", tr("ui.health.rsync_local_hint", nil))
	case lessVersion(localRsync, "3.1.0"):
		add("warn", tr("ui.health.rsync_local", nil), localRsync, tr("ui.health.rsync_progress_hint", nil))
	default:
		add("ok", tr("ui.health.rsync_local", nil), localRsync, "")
	}
	if output, err := c.client.Run([]string{"rsync", "--version"}, nil, 20*time.Second); err == nil {
		add("ok", tr("ui.health.rsync_nas", nil), transfer.ParseRsyncVersion(string(output)), "")
	}

	if _, err := exec.LookPath("inotifywait"); err != nil {
		add("warn", tr("ui.health.inotify", nil), tr("ui.health.inotify_missing", nil), tr("ui.health.inotify_hint", nil))
	} else {
		add("ok", tr("ui.health.inotify", nil), "inotifywait", "")
	}

	if c.logger != nil {
		if entries, err := c.logger.Tail(800); err == nil {
			cutoff := time.Now().Add(-24 * time.Hour)
			count := 0
			last := ""
			for _, entry := range entries {
				if entry.Level == "ERROR" && entry.Timestamp.After(cutoff) {
					count++
					last = entry.Detail
				}
			}
			if count > 0 {
				add("warn", tr("ui.health.errors", nil), tr("ui.health.errors_count", map[string]any{"count": count}),
					shortHint(last))
			} else {
				add("ok", tr("ui.health.errors", nil), tr("ui.health.errors_none", nil), "")
			}
		}
	}
	add("ok", tr("ui.health.client", nil), version.AppName+" "+version.AppVersion, "")
	return checks
}

func shortID(value string) string {
	if len(value) > 12 {
		return value[:12] + "…"
	}
	return valueOrDash(value)
}

// lessVersion compares dotted versions numerically ("3.1.2" < "3.10.0").
func lessVersion(left, right string) bool {
	a, b := strings.Split(left, "."), strings.Split(right, ".")
	for index := 0; index < len(a) || index < len(b); index++ {
		var x, y int
		if index < len(a) {
			fmt.Sscanf(a[index], "%d", &x)
		}
		if index < len(b) {
			fmt.Sscanf(b[index], "%d", &y)
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// buildHealthPane is the "Controlla il sistema" card in Settings › Info.
func (c *clientApp) buildHealthPane() fyne.CanvasObject {
	results := container.NewVBox()
	status := widget.NewLabel(tr("ui.health.intro", nil))
	status.Wrapping = fyne.TextWrapWord
	status.Importance = widget.LowImportance
	var run *widget.Button
	run = widget.NewButtonWithIcon(tr("ui.health.run", nil), theme.SearchIcon(), func() {
		run.Disable()
		status.SetText(tr("ui.health.running", nil))
		go func() {
			checks := c.runHealthChecks()
			fyne.Do(func() {
				run.Enable()
				results.Objects = nil
				problems := 0
				for _, check := range checks {
					results.Add(c.healthRow(check))
					if check.Level != "ok" {
						problems++
					}
				}
				results.Refresh()
				if problems == 0 {
					status.SetText(tr("ui.health.all_ok", nil))
				} else {
					status.SetText(tr("ui.health.problems", map[string]any{"count": problems}))
				}
			})
		}()
	})
	run.Importance = widget.HighImportance
	return c.listGroup(tr("ui.health.title", nil), tr("ui.health.subtitle", nil),
		container.NewBorder(nil, nil, nil, run, status), results)
}

func (c *clientApp) healthRow(check healthCheck) fyne.CanvasObject {
	var icon fyne.Resource = theme.NewSuccessThemedResource(theme.ConfirmIcon())
	switch check.Level {
	case "warn":
		icon = theme.NewWarningThemedResource(theme.WarningIcon())
	case "error":
		icon = theme.NewErrorThemedResource(theme.ErrorIcon())
	}
	title := widget.NewLabelWithStyle(check.Title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	detail := widget.NewLabel(check.Detail)
	detail.Truncation = fyne.TextTruncateEllipsis
	lines := container.NewVBox(container.NewBorder(nil, nil, title, nil, detail))
	if check.Hint != "" {
		hint := widget.NewLabel("→ " + check.Hint)
		hint.Wrapping = fyne.TextWrapWord
		hint.Importance = widget.LowImportance
		lines.Add(hint)
	}
	return container.NewBorder(nil, nil, widget.NewIcon(icon), nil, lines)
}
