// Package mirror implements external folders: one-way local -> NAS replicas.
package mirror

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/paths"
	"nasbox-go/internal/remote"
)

type Mirror struct {
	Source  string `json:"source"`
	Dest    string `json:"dest"`
	Enabled bool   `json:"enabled"`
	Error   string `json:"error"`
}

func Configured(cfg *config.Config) []Mirror {
	value := cfg.Get("mirrors")
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var mirrors []Mirror
	if err := json.Unmarshal(raw, &mirrors); err != nil {
		return nil
	}
	return mirrors
}

// SyncOnce runs every enabled mirror while holding the NAS lease.
func SyncOnce(cfg *config.Config, client *remote.Client) (int, error) {
	mirrors := Configured(cfg)
	if len(mirrors) == 0 {
		return 0, nil
	}
	deviceID := "go-client"
	release, err := client.AcquireLease(deviceID)
	if err != nil {
		return 0, err
	}
	defer release()
	synced := 0
	for _, item := range mirrors {
		if !item.Enabled || item.Source == "" || item.Dest == "" {
			continue
		}
		if err := paths.ValidateRelative(item.Dest); err != nil {
			continue
		}
		if info, err := os.Stat(item.Source); err != nil || !info.IsDir() {
			continue
		}
		remoteDir := strings.TrimSuffix(cfg.RemotePrefix(), "/") + "/" + strings.Trim(item.Dest, "/")
		args := []string{"-a", "--delete", "--exclude", `*.sync-trash/`, "--exclude", `*conflitto\ da\ *`}
		if limit := cfg.Int("bandwidth_kbps"); limit > 0 {
			args = append(args, "--bwlimit", strconv.Itoa(limit))
		}
		sshArgs := "-o BatchMode=yes -o ConnectTimeout=10"
		if port := cfg.Int("ssh_port"); port > 0 && port != 22 {
			sshArgs += fmt.Sprintf(" -p %d", port)
		}
		args = append(args, "-e", "ssh "+sshArgs, item.Source+"/",
			fmt.Sprintf("%s@%s:%s/", cfg.String("nas_user"), cfg.String("nas_lan"), remoteDir))
		cmd := exec.Command("rsync", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			continue
		}
		synced++
		writeStamp(item.Source)
	}
	return synced, nil
}

func writeStamp(source string) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "sync-daemon", "watch-stamps")
	_ = os.MkdirAll(dir, 0o700)
	name := strings.NewReplacer("/", "_", " ", "_").Replace(source) + ".stamp"
	_ = os.WriteFile(filepath.Join(dir, name), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
}
