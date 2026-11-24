// Package integration adds NASBox to the desktop's file managers: a context
// menu entry "NASBox: versioni precedenti" (Thunar, Nautilus, Nemo, Dolphin,
// Caja) and status emblems on the NASBox folder and on conflict copies.
// Everything is per-user, reversible (Remove) and best-effort: a file
// manager that is not installed is simply skipped.
package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	actionID   = "nasbox-go-versions"
	actionName = "NASBox: versioni precedenti"
	actionHelp = "Mostra e ripristina le versioni precedenti del file (su questo PC e sul NAS)"
)

// Result reports what Install/Remove changed, per file manager.
type Result struct {
	Changed []string
	Skipped []string
}

func home() string {
	if value, err := os.UserHomeDir(); err == nil {
		return value
	}
	return os.Getenv("HOME")
}

func configHome() string {
	if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
		return value
	}
	return filepath.Join(home(), ".config")
}

func dataHome() string {
	if value := os.Getenv("XDG_DATA_HOME"); value != "" {
		return value
	}
	return filepath.Join(home(), ".local", "share")
}

// present reports whether a file manager is installed (binary on PATH).
var present = func(binary string) bool {
	_, err := exec.LookPath(binary)
	return err == nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func writeFile(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".nasbox-tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Install adds the context-menu entry to every installed file manager.
// binary is the absolute path of the nasbox-go executable.
func Install(binary string) (Result, error) {
	result := Result{}
	steps := []struct {
		name, binary string
		apply        func(string) error
	}{
		{"Thunar", "thunar", installThunar},
		{"Nautilus", "nautilus", installNautilus},
		{"Nemo", "nemo", installNemo},
		{"Dolphin", "dolphin", installDolphin},
		{"Caja", "caja", installCaja},
	}
	for _, step := range steps {
		if !present(step.binary) {
			result.Skipped = append(result.Skipped, step.name)
			continue
		}
		if err := step.apply(binary); err != nil {
			return result, fmt.Errorf("%s: %w", step.name, err)
		}
		result.Changed = append(result.Changed, step.name)
	}
	return result, nil
}

// Remove undoes Install everywhere (files that do not exist are ignored).
func Remove() (Result, error) {
	result := Result{}
	if changed, err := removeThunar(); err != nil {
		return result, fmt.Errorf("Thunar: %w", err)
	} else if changed {
		result.Changed = append(result.Changed, "Thunar")
	}
	for name, path := range map[string]string{
		"Nautilus": nautilusScript(), "Nemo": nemoAction(), "Dolphin": dolphinMenu(), "Caja": cajaScript(),
	} {
		if err := os.Remove(path); err == nil {
			result.Changed = append(result.Changed, name)
		} else if !os.IsNotExist(err) {
			return result, fmt.Errorf("%s: %w", name, err)
		}
	}
	return result, nil
}

// Installed reports whether at least one entry is installed.
func Installed() bool {
	for _, path := range []string{nautilusScript(), nemoAction(), dolphinMenu(), cajaScript()} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	content, err := os.ReadFile(thunarFile())
	return err == nil && strings.Contains(string(content), "<unique-id>"+actionID+"</unique-id>")
}

// ---------------------------------------------------------------- Thunar

func thunarFile() string { return filepath.Join(configHome(), "Thunar", "uca.xml") }

// withoutOurThunarAction removes every <action> block carrying our
// unique-id, leaving the user's own actions byte-for-byte untouched.
func withoutOurThunarAction(text string) string {
	marker := "<unique-id>" + actionID + "</unique-id>"
	var out strings.Builder
	rest := text
	for {
		start := strings.Index(rest, "<action>")
		if start < 0 {
			out.WriteString(rest)
			return out.String()
		}
		end := strings.Index(rest[start:], "</action>")
		if end < 0 {
			out.WriteString(rest)
			return out.String()
		}
		end += start + len("</action>")
		block := rest[start:end]
		if strings.Contains(block, marker) {
			out.WriteString(strings.TrimRight(rest[:start], " \t"))
			rest = strings.TrimPrefix(rest[end:], "\n")
			continue
		}
		out.WriteString(rest[:end])
		rest = rest[end:]
	}
}

func installThunar(binary string) error {
	path := thunarFile()
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		content = []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<actions>\n</actions>\n")
	} else if err != nil {
		return err
	} else {
		backup := path + ".nasbox-backup"
		if _, statErr := os.Stat(backup); os.IsNotExist(statErr) {
			if err := os.WriteFile(backup, content, 0o644); err != nil {
				return err
			}
		}
	}
	text := withoutOurThunarAction(string(content))
	end := strings.LastIndex(text, "</actions>")
	if end < 0 {
		return fmt.Errorf("uca.xml non riconosciuto")
	}
	action := fmt.Sprintf(`<action>
	<icon>folder-sync</icon>
	<name>%s</name>
	<submenu></submenu>
	<unique-id>%s</unique-id>
	<command>%s versions %%f</command>
	<description>%s</description>
	<range>*</range>
	<patterns>*</patterns>
	<other-files/>
	<text-files/>
	<image-files/>
	<audio-files/>
	<video-files/>
</action>
`, actionName, actionID, xmlEscape(shellQuote(binary)), actionHelp)
	text = text[:end] + action + text[end:]
	return writeFile(path, text, 0o644)
}

func removeThunar() (bool, error) {
	path := thunarFile()
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	text := withoutOurThunarAction(string(content))
	if text == string(content) {
		return false, nil
	}
	return true, writeFile(path, text, 0o644)
}

func xmlEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

// ------------------------------------------------- Nautilus / Caja scripts

func nautilusScript() string {
	return filepath.Join(dataHome(), "nautilus", "scripts", actionName)
}

func cajaScript() string { return filepath.Join(configHome(), "caja", "scripts", actionName) }

func scriptBody(binary, variable string) string {
	return fmt.Sprintf("#!/bin/sh\n# %s (installato da NASBox)\nfirst=$(printf '%%s\\n' \"$%s\" | head -n 1)\n[ -n \"$first\" ] || first=\"$1\"\nexec %s versions \"$first\"\n",
		actionHelp, variable, shellQuote(binary))
}

func installNautilus(binary string) error {
	return writeFile(nautilusScript(), scriptBody(binary, "NAUTILUS_SCRIPT_SELECTED_FILE_PATHS"), 0o755)
}

func installCaja(binary string) error {
	return writeFile(cajaScript(), scriptBody(binary, "CAJA_SCRIPT_SELECTED_FILE_PATHS"), 0o755)
}

// --------------------------------------------------------------- Nemo

func nemoAction() string {
	return filepath.Join(dataHome(), "nemo", "actions", actionID+".nemo_action")
}

func installNemo(binary string) error {
	return writeFile(nemoAction(), fmt.Sprintf(`[Nemo Action]
Name=%s
Comment=%s
Exec=%s versions %%F
Icon-Name=folder-sync
Selection=s
Extensions=any;
`, actionName, actionHelp, shellQuote(binary)), 0o644)
}

// ------------------------------------------------------------- Dolphin

func dolphinMenu() string {
	return filepath.Join(dataHome(), "kio", "servicemenus", actionID+".desktop")
}

func installDolphin(binary string) error {
	return writeFile(dolphinMenu(), fmt.Sprintf(`[Desktop Entry]
Type=Service
MimeType=application/octet-stream;all/allfiles;
X-KDE-ServiceTypes=KonqPopupMenu/Plugin
Actions=nasboxVersions

[Desktop Action nasboxVersions]
Name=%s
Icon=folder-sync
Exec=%s versions %%f
`, actionName, shellQuote(binary)), 0o755)
}

// ------------------------------------------------------------- Emblems

// Emblem names from the freedesktop icon naming spec, available in the
// common icon themes and shown by Thunar, Nautilus, Nemo and Caja.
const (
	EmblemSynced  = "emblem-default"
	EmblemSyncing = "emblem-synchronizing"
	EmblemProblem = "emblem-important"
)

var runGio = func(args ...string) error {
	command := exec.Command("gio", args...)
	done := make(chan error, 1)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		return fmt.Errorf("gio non risponde")
	}
}

// SetEmblem marks a file or folder with one emblem ("" clears it). Silently
// does nothing when gio/GVFS metadata is unavailable.
func SetEmblem(path, emblem string) {
	if !present("gio") {
		return
	}
	if emblem == "" {
		_ = runGio("set", "-t", "unset", path, "metadata::emblems")
		return
	}
	_ = runGio("set", "-t", "stringv", path, "metadata::emblems", emblem)
}
