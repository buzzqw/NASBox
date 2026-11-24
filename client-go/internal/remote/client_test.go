package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nasbox-go/internal/config"
)

func TestParsePairedStatus(t *testing.T) {
	data := []byte("RESTORE_BATCH_V1\x002\x00a.txt\x00RESTORED\x00b.txt\x00NO_VERSION\x00")
	got, err := parsePairedStatus(data, "RESTORE_BATCH_V1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a.txt": "RESTORED", "b.txt": "NO_VERSION"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestShellQuoteKeepsSimplePathsReadable(t *testing.T) {
	if got := shellQuote("/volume1/NASBox"); got != "/volume1/NASBox" {
		t.Fatalf("simple path was quoted: %q", got)
	}
	quoted := shellQuote("a b'c")
	if quoted != `'a b'\''c'` {
		t.Fatalf("unexpected quoting: %q", quoted)
	}
}

func TestRestoreVersionNormalisesSuffix(t *testing.T) {
	version := normaliseVersionSuffix("-2026-11-24--07-00-00")
	if version != "2026-11-24--07-00-00-000000Z" {
		t.Fatalf("bad normalisation: %q", version)
	}
}

func TestNulJoin(t *testing.T) {
	if got := string(nulJoin([][]byte{[]byte("a"), []byte("b")})); got != "a\x00b\x00" {
		t.Fatalf("bad NUL framing: %q", got)
	}
}

func testConfig() *config.Config {
	cfg := config.New("")
	cfg.Set("nas_lan", "192.168.1.119", false)
	cfg.Set("nas_wan", "wan.nasbox.example", false)
	cfg.Set("nas_user", "andres", false)
	cfg.Set("jump_host", "bastion.example", false)
	cfg.Set("jump_user", "jumpuser", false)
	return cfg
}

func deterministicMultiplex(t *testing.T) {
	t.Helper()
	old := multiplexSocketDirFn
	multiplexSocketDirFn = func() string { return "/tmp/nasbox-ssh-1000" }
	t.Cleanup(func() { multiplexSocketDirFn = old })
}

func fakeRunCommand(t *testing.T, fn commandFunc) {
	t.Helper()
	old := runCommand
	runCommand = fn
	t.Cleanup(func() { runCommand = old })
}

func TestSSHBuilderLANOptions(t *testing.T) {
	deterministicMultiplex(t)
	b := NewSSHBuilder(testConfig(), Connection{Host: "192.168.1.119"})
	want := []string{
		"-p", "22",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=30",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/tmp/nasbox-ssh-1000/cm-%C",
		"-o", "ControlPersist=60",
	}
	if got := b.Options(); !reflect.DeepEqual(got, want) {
		t.Fatalf("LAN options:\n got %q\nwant %q", got, want)
	}
	if got := b.Target(); got != "andres@192.168.1.119" {
		t.Fatalf("bad target: %q", got)
	}
	wantCommand := append([]string{"ssh"}, want...)
	wantCommand = append(wantCommand, "andres@192.168.1.119", "/volume1/NASBox/sync-daemon-server.sh --print-config")
	if got := b.Args("/volume1/NASBox/sync-daemon-server.sh --print-config"); !reflect.DeepEqual(got, wantCommand) {
		t.Fatalf("LAN command:\n got %q\nwant %q", got, wantCommand)
	}
}

func TestSSHBuilderWANJumpAndPorts(t *testing.T) {
	deterministicMultiplex(t)
	cfg := testConfig()
	cfg.Set("ssh_port", 2222, false)
	cfg.Set("jump_port", 2223, false)

	direct := NewSSHBuilder(cfg, Connection{Host: "wan.nasbox.example"})
	directOpts := strings.Join(direct.Options(), " ")
	if !strings.Contains(directOpts, "-p 2222") {
		t.Fatalf("WAN missing explicit ssh port: %s", directOpts)
	}
	if strings.Contains(directOpts, "ProxyJump=") {
		t.Fatalf("direct WAN must not use ProxyJump: %s", directOpts)
	}
	if got := direct.Target(); got != "andres@wan.nasbox.example" {
		t.Fatalf("bad WAN target: %q", got)
	}

	viaJump := NewSSHBuilder(cfg, Connection{Host: "192.168.1.119", ViaJump: true})
	jumpOpts := strings.Join(viaJump.Options(), " ")
	for _, want := range []string{
		"-p 2222", "ProxyJump=jumpuser@bastion.example:2223", "BatchMode=yes",
	} {
		if !strings.Contains(jumpOpts, want) {
			t.Fatalf("jump options missing %s: %s", want, jumpOpts)
		}
	}
	if got := viaJump.Target(); got != "andres@192.168.1.119" {
		t.Fatalf("ProxyJump must still target the NAS LAN: %q", got)
	}

	cfg.Set("jump_user", "", false)
	fallback := NewSSHBuilder(cfg, Connection{Host: "192.168.1.119", ViaJump: true})
	if !strings.Contains(strings.Join(fallback.Options(), " "), "ProxyJump=andres@bastion.example:2223") {
		t.Fatalf("jump_user fallback to nas_user failed: %q", fallback.Options())
	}
}

func TestSSHBuilderPinnedKnownHostsQuoted(t *testing.T) {
	deterministicMultiplex(t)
	cfg := testConfig()
	cfg.Set("ssh_host_key_pinned", true, false)
	cfg.Set("ssh_known_hosts", "/home/andres/.config/sync-daemon/known hosts", false)
	b := NewSSHBuilder(cfg, Connection{Host: "192.168.1.119"})
	opts := strings.Join(b.Options(), " ")
	for _, want := range []string{"StrictHostKeyChecking=yes", "UserKnownHostsFile=/home/andres/.config/sync-daemon/known hosts"} {
		if !strings.Contains(opts, want) {
			t.Fatalf("pinned options missing %s: %s", want, opts)
		}
	}
	if strings.Contains(opts, "accept-new") {
		t.Fatalf("pinned connection must not accept new keys: %s", opts)
	}
	wantEArg := "ssh -p 22 -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=15" +
		" -o ServerAliveInterval=30 -o ControlMaster=auto" +
		" -o ControlPath=/tmp/nasbox-ssh-1000/cm-%C -o ControlPersist=60" +
		" -o 'UserKnownHostsFile=/home/andres/.config/sync-daemon/known hosts'"
	if got := b.EArg(); got != wantEArg {
		t.Fatalf("bad -e argument:\n got %q\nwant %q", got, wantEArg)
	}

	cfg.Set("ssh_host_key_pinned", false, false)
	cfg.Set("ssh_jump_key_pinned", true, false)
	jumpPin := NewSSHBuilder(cfg, Connection{Host: "192.168.1.119", ViaJump: true})
	if !strings.Contains(strings.Join(jumpPin.Options(), " "), "StrictHostKeyChecking=yes") {
		t.Fatalf("jump pin must force strict checking: %q", jumpPin.Options())
	}
}

func TestShlexQuoteAndRemoteSpec(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", "''"},
		{"/volume1/NASBox", "/volume1/NASBox"},
		{"a b'c", `'a b'\''c'`},
		{"-o StrictHostKeyChecking=yes", "'-o StrictHostKeyChecking=yes'"},
		{"user@host:22/path,C=%", "user@host:22/path,C=%"},
	} {
		if got := shlexQuote(tc.input); got != tc.want {
			t.Fatalf("shlexQuote(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	deterministicMultiplex(t)
	b := NewSSHBuilder(testConfig(), Connection{Host: "192.168.1.119"})
	if got, want := b.RemoteSpec("/volume1/My NASBox/"), "andres@192.168.1.119:'/volume1/My NASBox/'"; got != want {
		t.Fatalf("remote spec = %q, want %q", got, want)
	}
}

func TestResolveConnectionPriority(t *testing.T) {
	old := probePort
	t.Cleanup(func() { probePort = old })
	cfg := testConfig()
	cfg.Set("jump_port", 2223, false)

	for _, tc := range []struct {
		name      string
		reachable map[string]bool
		want      Connection
		wantErr   bool
		wantProbe string
	}{
		{
			name: "LAN diretta", reachable: map[string]bool{"192.168.1.119": true},
			want: Connection{Host: "192.168.1.119"}, wantProbe: "192.168.1.119:22",
		},
		{
			name: "WAN diretta", reachable: map[string]bool{"wan.nasbox.example": true},
			want: Connection{Host: "wan.nasbox.example"}, wantProbe: "wan.nasbox.example:22",
		},
		{
			name: "bastione", reachable: map[string]bool{"bastion.example": true},
			want: Connection{Host: "192.168.1.119", ViaJump: true}, wantProbe: "bastion.example:2223",
		},
		{
			name: "nessuna rotta", reachable: map[string]bool{}, wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var probes []string
			probePort = func(host string, port int, _ time.Duration) bool {
				probes = append(probes, fmt.Sprintf("%s:%d", host, port))
				return tc.reachable[host]
			}
			got, err := ResolveConnection(cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected unreachable error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("connection = %+v, want %+v", got, tc.want)
			}
			if len(probes) == 0 || probes[len(probes)-1] != tc.wantProbe {
				t.Fatalf("last probe = %v, want %s", probes, tc.wantProbe)
			}
		})
	}
}

func TestHostKeyFingerprintsSynthetic(t *testing.T) {
	var calls [][]string
	fakeRunCommand(t, func(_ context.Context, stdin []byte, name string, args ...string) ([]byte, []byte, error) {
		calls = append(calls, append([]string{name}, args...))
		switch name {
		case "ssh-keyscan":
			return []byte("# comment\n192.168.1.119 ssh-ed25519 AAAAKEY1\n192.168.1.119 ssh-rsa AAAAKEY2\n"), nil, nil
		case "ssh-keygen":
			if want := "ssh-ed25519 AAAAKEY1\nssh-rsa AAAAKEY2\n"; string(stdin) != want {
				return nil, nil, fmt.Errorf("unexpected ssh-keygen stdin %q", stdin)
			}
			return []byte("256 SHA256:fp1 comment (ED25519)\n256 SHA256:fp1 duplicate\n256 SHA256:fp2 comment (RSA)\n"), nil, nil
		}
		return nil, nil, errors.New("unexpected command " + name)
	})
	got, err := HostKeyFingerprints(context.Background(), "192.168.1.119", 22)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"SHA256:fp1", "SHA256:fp2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fingerprints = %q, want %q", got, want)
	}
	if want := []string{"ssh-keyscan", "-T", "6", "-p", "22", "192.168.1.119"}; !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("keyscan argv = %q, want %q", calls[0], want)
	}
	if want := []string{"ssh-keygen", "-lf", "-", "-E", "sha256"}; !reflect.DeepEqual(calls[1], want) {
		t.Fatalf("keygen argv = %q, want %q", calls[1], want)
	}
}

func TestPinHostKeyRejectsUnseenFingerprint(t *testing.T) {
	dir := t.TempDir()
	knownHosts := filepath.Join(dir, "known", "known_hosts")
	fakeRunCommand(t, func(_ context.Context, _ []byte, name string, _ ...string) ([]byte, []byte, error) {
		if name == "ssh-keyscan" {
			return []byte("192.168.1.119 ssh-ed25519 AAAAKEY1\n"), nil, nil
		}
		return []byte("256 SHA256:fp1 comment (ED25519)\n"), nil, nil
	})
	if _, err := PinHostKey(context.Background(), "192.168.1.119", 22, knownHosts, "SHA256:wrong"); err == nil {
		t.Fatal("expected mismatch error")
	} else if !strings.Contains(err.Error(), "non corrisponde") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(knownHosts); !os.IsNotExist(err) {
		t.Fatalf("known_hosts must not be written on mismatch: %v", err)
	}
}

func TestPinHostKeyWritesDedicatedFile(t *testing.T) {
	dir := t.TempDir()
	knownHosts := filepath.Join(dir, "known", "known_hosts")
	fakeRunCommand(t, func(_ context.Context, _ []byte, name string, _ ...string) ([]byte, []byte, error) {
		if name == "ssh-keyscan" {
			return []byte("192.168.1.119 ssh-ed25519 AAAAKEY1\n"), nil, nil
		}
		return []byte("256 SHA256:fp1 comment (ED25519)\n"), nil, nil
	})
	path, err := PinHostKey(context.Background(), "192.168.1.119", 22, knownHosts, "SHA256:fp1")
	if err != nil {
		t.Fatal(err)
	}
	if path != knownHosts {
		t.Fatalf("path = %q, want %q", path, knownHosts)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "ssh-ed25519 AAAAKEY1") {
		t.Fatalf("pinned key missing: %q", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts mode = %o, want 600", info.Mode().Perm())
	}
}

func TestValidateHostKeyPin(t *testing.T) {
	dir := t.TempDir()
	knownHosts := filepath.Join(dir, "known_hosts")
	line := "192.168.1.119 ssh-ed25519 AAAAKEY1\n"
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.Set("ssh_host_key_pinned", true, false)
	cfg.Set("ssh_known_hosts", knownHosts, false)
	client := NewWithConnection(cfg, Connection{Host: "192.168.1.119"})

	liveKey := "AAAAKEY1"
	fakeRunCommand(t, func(_ context.Context, stdin []byte, name string, _ ...string) ([]byte, []byte, error) {
		if name == "ssh-keyscan" {
			return []byte("192.168.1.119 ssh-ed25519 " + liveKey + "\n"), nil, nil
		}
		if strings.Contains(string(stdin), "AAAAKEY1") {
			return []byte("256 SHA256:fp1 comment (ED25519)\n"), nil, nil
		}
		return []byte("256 SHA256:fp2 comment (ED25519)\n"), nil, nil
	})
	if err := client.ValidateHostKeyPin(context.Background()); err != nil {
		t.Fatalf("matching pin rejected: %v", err)
	}
	liveKey = "AAAAKEY2"
	if err := client.ValidateHostKeyPin(context.Background()); err == nil {
		t.Fatal("changed host key accepted")
	} else if !strings.Contains(err.Error(), "non corrisponde al pin") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := client.RunLocked([]string{"cat", "x"}, nil, "dev", time.Second); err == nil {
		t.Fatal("mutating operation must fail when the pin no longer matches")
	}
}

func TestValidateHostKeyPinRequiresKnownHosts(t *testing.T) {
	cfg := testConfig()
	cfg.Set("ssh_host_key_pinned", true, false)
	client := NewWithConnection(cfg, Connection{Host: "192.168.1.119"})
	if err := client.ValidateHostKeyPin(context.Background()); err == nil {
		t.Fatal("expected explicit configuration error")
	} else if !strings.Contains(err.Error(), "ssh_known_hosts") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunUsesSharedBuilderAndQuotesArgs(t *testing.T) {
	deterministicMultiplex(t)
	cfg := testConfig()
	cfg.Set("remote_server_script", "/volume1/NASBox/sync-daemon-server.sh", false)
	client := NewWithConnection(cfg, Connection{Host: "192.168.1.119", ViaJump: true})
	var got []string
	fakeRunCommand(t, func(_ context.Context, _ []byte, name string, args ...string) ([]byte, []byte, error) {
		got = append([]string{name}, args...)
		return []byte("ok"), nil, nil
	})
	if _, err := client.Run([]string{cfg.RemoteScript(), "--browse-list", "folder with space"}, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	wantCommand := client.Builder().Args(
		cfg.RemoteScript() + " --browse-list 'folder with space'",
	)
	if !reflect.DeepEqual(got, wantCommand) {
		t.Fatalf("ssh command:\n got %q\nwant %q", got, wantCommand)
	}
}

func TestCleanSSHStderr(t *testing.T) {
	raw := "** WARNING: connection is not using a post-quantum key exchange algorithm\nreal failure\n"
	if got := cleanSSHStderr(raw); got != "real failure" {
		t.Fatalf("cleaned stderr = %q", got)
	}
}
