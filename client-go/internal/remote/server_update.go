package remote

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// Server self-update, as in the Python client: a new server is published on
// the NAS as sync-daemon-server-<version>.sh next to the active script; the
// client copies it over the active one, restarts the daemon and verifies the
// running version.

var serverUpdateName = regexp.MustCompile(`^sync-daemon-server-(\d+\.\d+\.\d+)\.sh$`)

// ServerUpdate is a versioned server script found beside the active one.
type ServerUpdate struct {
	Path    string
	Version string
}

// FindServerUpdate returns the newest versioned script strictly newer than
// current, or nil when there is none.
func (c *Client) FindServerUpdate(current string) (*ServerUpdate, error) {
	active := c.script()
	if active == "" {
		return nil, fmt.Errorf("script server non configurato")
	}
	dir := path.Dir(active)
	listing := fmt.Sprintf(`for candidate in %s/sync-daemon-server-*.sh; do [ -f "$candidate" ] && printf '%%s\n' "$candidate"; done; true`,
		shellQuote(dir))
	output, err := c.Run([]string{"sh", "-c", listing}, nil, 20*time.Second)
	if err != nil {
		return nil, err
	}
	return newestServerUpdate(string(output), dir, current), nil
}

func newestServerUpdate(listing, dir, current string) *ServerUpdate {
	var best *ServerUpdate
	for _, line := range strings.Split(listing, "\n") {
		candidate := strings.TrimSpace(line)
		if candidate == "" || path.Dir(candidate) != dir {
			continue
		}
		match := serverUpdateName.FindStringSubmatch(path.Base(candidate))
		if match == nil || !versionLess(current, match[1]) {
			continue
		}
		if best == nil || versionLess(best.Version, match[1]) {
			best = &ServerUpdate{Path: candidate, Version: match[1]}
		}
	}
	return best
}

// InstallServerUpdate replaces the active script with update, restarts the
// daemon and checks that the expected version is running. The previous
// script is kept as <script>.prev for a manual rollback.
func (c *Client) InstallServerUpdate(update ServerUpdate) error {
	active := c.script()
	match := serverUpdateName.FindStringSubmatch(path.Base(update.Path))
	if active == "" || path.Dir(update.Path) != path.Dir(active) || match == nil || match[1] != update.Version {
		return fmt.Errorf("percorso dello script server versionato non valido")
	}
	quotedActive, quotedSource := shellQuote(active), shellQuote(update.Path)
	install := fmt.Sprintf(
		"cp -f %s %s.prev && cp -f %s %s.new && chmod +x %s.new && mv -f %s.new %s && %s --restart >/dev/null && %s --print-config",
		quotedActive, quotedActive, quotedSource, quotedActive, quotedActive, quotedActive, quotedActive,
		quotedActive, quotedActive)
	output, err := c.Run([]string{"sh", "-c", install}, nil, 2*time.Minute)
	if err != nil {
		return err
	}
	values := parseKeyValues(string(output))
	if values["VERSION"] != update.Version || strings.ToLower(values["RUNNING"]) != "true" {
		return fmt.Errorf("verifica server fallita: VERSION=%s RUNNING=%s", values["VERSION"], values["RUNNING"])
	}
	return nil
}

func parseKeyValues(output string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = value
		}
	}
	return values
}
