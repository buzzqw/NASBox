package transfer

import (
	"strings"
	"testing"

	"nasbox-go/internal/config"
	"nasbox-go/internal/remote"
)

func TestBuildCommandUsesDeltaFriendlyFlags(t *testing.T) {
	cfg := config.New("")
	cfg.Set("local_root", "/home/user/NASBox", false)
	cfg.Set("remote_prefix", "/volume1/NASBox", false)
	cfg.Set("nas_lan", "192.168.1.119", false)
	cfg.Set("nas_user", "andres", false)
	client := remote.New(cfg)
	tr := New(cfg, client)
	args, payload := tr.BuildCommand([]string{"a.txt", "folder/b.txt"}, true)
	joined := strings.Join(args, " ")
	for _, flag := range []string{"--partial", "--fuzzy", "--delay-updates", "--files-from=-", "--from0"} {
		if !strings.Contains(joined, flag) {
			t.Fatalf("missing %s in %s", flag, joined)
		}
	}
	if !strings.HasSuffix(joined, "/home/user/NASBox andres@192.168.1.119:/volume1/NASBox/") {
		t.Fatalf("bad endpoints: %s", joined)
	}
	if string(payload) != "a.txt\x00folder/b.txt\x00" {
		t.Fatalf("bad NUL payload: %q", payload)
	}
}
