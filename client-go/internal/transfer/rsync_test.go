package transfer

import (
	"context"
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

func baseConfig() *config.Config {
	cfg := config.New("")
	cfg.Set("local_root", "/home/user/NASBox", false)
	cfg.Set("remote_prefix", "/volume1/NASBox", false)
	cfg.Set("nas_lan", "192.168.1.119", false)
	cfg.Set("nas_user", "andres", false)
	return cfg
}

func captureRsync(t *testing.T) (*[]string, *[]byte) {
	t.Helper()
	old := runRsync
	var args []string
	var stdin []byte
	runRsync = func(_ context.Context, in []byte, argv ...string) ([]byte, error) {
		args = append([]string(nil), argv...)
		stdin = append([]byte(nil), in...)
		return nil, nil
	}
	t.Cleanup(func() { runRsync = old })
	return &args, &stdin
}

func hasArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func TestBuildCommandUsesSharedSSHBuilder(t *testing.T) {
	cfg := baseConfig()
	cfg.Set("ssh_port", 2222, false)
	cfg.Set("jump_host", "bastion.example", false)
	cfg.Set("jump_user", "jumpuser", false)
	cfg.Set("ssh_host_key_pinned", true, false)
	cfg.Set("ssh_known_hosts", "/home/andres/known hosts", false)
	client := remote.NewWithConnection(cfg, remote.Connection{Host: "192.168.1.119", ViaJump: true})
	tr := New(cfg, client)

	args, _ := tr.BuildCommand([]string{"a.txt"}, true)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--protect-args",
		"-e " + client.Builder().EArg(),
		"-p 2222",
		"ProxyJump=jumpuser@bastion.example:22",
		"StrictHostKeyChecking=yes",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("command missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("pinned transfer must not accept new keys: %s", joined)
	}
	if strings.Contains(joined, "ssh -o BatchMode=yes -o ConnectTimeout=10") {
		t.Fatalf("stale ad-hoc ssh options leaked into command: %s", joined)
	}
}

func TestRemoteSpecQuotesSpaces(t *testing.T) {
	cfg := baseConfig()
	cfg.Set("remote_prefix", "/volume1/My NASBox", false)
	client := remote.NewWithConnection(cfg, remote.Connection{Host: "192.168.1.119"})
	tr := New(cfg, client)
	args, _ := tr.BuildCommand([]string{"a.txt"}, true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "andres@192.168.1.119:'/volume1/My NASBox/'") {
		t.Fatalf("remote path with spaces not quoted: %s", joined)
	}
}

func TestUploadUsesSharedBuilderAndProtectArgs(t *testing.T) {
	cfg := baseConfig()
	client := remote.NewWithConnection(cfg, remote.Connection{Host: "192.168.1.119"})
	tr := New(cfg, client)
	argsPtr, stdinPtr := captureRsync(t)

	if err := tr.Upload([]string{"a.txt", "folder/b.txt"}); err != nil {
		t.Fatal(err)
	}
	args, stdin := *argsPtr, *stdinPtr
	if !hasArg(args, "--protect-args") {
		t.Fatalf("missing --protect-args: %q", args)
	}
	if !hasArg(args, client.Builder().EArg()) {
		t.Fatalf("rsync -e did not come from the shared builder: %q", args)
	}
	if string(stdin) != "a.txt\x00folder/b.txt\x00" {
		t.Fatalf("bad NUL payload: %q", stdin)
	}
	if !strings.HasSuffix(args[len(args)-1], "andres@192.168.1.119:/volume1/NASBox/") {
		t.Fatalf("bad remote endpoint: %q", args[len(args)-1])
	}
}

func TestDownloadPathUsesSharedBuilderAndProtectArgs(t *testing.T) {
	cfg := baseConfig()
	cfg.Set("local_root", t.TempDir(), false)
	client := remote.NewWithConnection(cfg, remote.Connection{Host: "192.168.1.119"})
	tr := New(cfg, client)
	argsPtr, _ := captureRsync(t)

	if err := tr.DownloadPath("folder with space/file.txt"); err != nil {
		t.Fatal(err)
	}
	args := *argsPtr
	wantSpec := client.Builder().RemoteSpec("/volume1/NASBox/folder with space/file.txt")
	if !hasArg(args, wantSpec) {
		t.Fatalf("remote spec = %q, want %q", args[len(args)-1], wantSpec)
	}
	if !hasArg(args, "--protect-args") || !hasArg(args, client.Builder().EArg()) {
		t.Fatalf("download did not use the shared SSH builder: %q", args)
	}
}

func TestHostKeyPinMismatchBlocksTransfer(t *testing.T) {
	cfg := baseConfig()
	cfg.Set("ssh_host_key_pinned", true, false)
	client := remote.NewWithConnection(cfg, remote.Connection{Host: "192.168.1.119"})
	tr := New(cfg, client)
	argsPtr, _ := captureRsync(t)

	err := tr.Upload([]string{"a.txt"})
	if err == nil || !strings.Contains(err.Error(), "ssh_known_hosts") {
		t.Fatalf("expected explicit pin configuration error, got %v", err)
	}
	if len(*argsPtr) != 0 {
		t.Fatalf("rsync must not start when the pin is unverified: %q", *argsPtr)
	}
}
