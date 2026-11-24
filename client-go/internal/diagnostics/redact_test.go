package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactTextRemovesCredentialsAndKeyMaterial(t *testing.T) {
	privateKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nSUPERSECRETKEYBODY\n-----END OPENSSH PRIVATE KEY-----"
	input := strings.Join([]string{
		"password=hunter2",
		`"api_key": "two words"`,
		"https://example.invalid/?token=query-secret&keep=yes",
		"Authorization: Bearer bearer-secret",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPUBLIC public@host",
		privateKey,
		"ordinary=value",
	}, "\n")
	got := RedactText(input)
	for _, secret := range []string{
		"hunter2", "two words", "query-secret", "bearer-secret",
		"AAAAC3NzaC1lZDI1NTE5AAAAIPUBLIC", "SUPERSECRETKEYBODY",
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret %q survived redaction:\n%s", secret, got)
		}
	}
	for _, keep := range []string{"keep=yes", "ordinary=value", redacted} {
		if !strings.Contains(got, keep) {
			t.Fatalf("expected %q in redacted text:\n%s", keep, got)
		}
	}
}

func TestRedactTextHandlesPrivateKeyInsideJSONString(t *testing.T) {
	key := "-----BEGIN RSA PRIVATE KEY-----\nPRIVATEBODY\n-----END RSA PRIVATE KEY-----"
	encoded, err := json.Marshal(map[string]string{"detail": key})
	if err != nil {
		t.Fatal(err)
	}
	got := RedactText(string(encoded))
	if strings.Contains(got, "PRIVATEBODY") || !strings.Contains(got, redacted) {
		t.Fatalf("escaped private key survived: %s", got)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("redacting an escaped key broke JSON: %v\n%s", err, got)
	}
}

func TestRedactJSONSanitisesNestedConfigValues(t *testing.T) {
	input := []byte(`{
		"nas_user": "andres",
		"local_root": "/home/andres/NASBox",
		"password": "config-secret",
		"ssh_known_hosts": "nas.example ssh-ed25519 AAAAKNOWN",
		"private_key_path": "/home/andres/.ssh/id_ed25519",
		"mirrors": [{"source": "/data", "token": "mirror-secret", "enabled": true}],
		"nested": {"credential": "nested-secret", "note": "api_key=note-secret"},
		"monkey": "banana"
	}`)
	got, err := RedactJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"config-secret", "AAAAKNOWN", "id_ed25519", "mirror-secret",
		"nested-secret", "note-secret",
	} {
		if strings.Contains(string(got), secret) {
			t.Fatalf("secret %q survived config redaction:\n%s", secret, got)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["nas_user"] != "andres" || decoded["local_root"] != "/home/andres/NASBox" ||
		decoded["password"] != redacted || decoded["monkey"] != "banana" {
		t.Fatalf("non-secret values changed: %v", decoded)
	}
	mirrors := decoded["mirrors"].([]any)
	mirror := mirrors[0].(map[string]any)
	if mirror["token"] != redacted || mirror["source"] != "/data" || mirror["enabled"] != true {
		t.Fatalf("nested mirror was not sanitised correctly: %v", mirror)
	}
}

func TestRedactJSONAndJSONLTolerateBrokenInputWithoutLeaking(t *testing.T) {
	got, err := RedactJSON([]byte(`{"password": "broken-secret"`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "broken-secret") || !strings.Contains(string(got), redacted) {
		t.Fatalf("broken JSON leaked a secret: %s", got)
	}

	input := []byte("{\"detail\":\"token=line-secret\"}\nnot-json password=invalid-secret\n\n")
	lines := strings.Split(strings.TrimSpace(string(RedactJSONL(input))), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two non-empty JSON lines, got %d: %q", len(lines), lines)
	}
	for _, line := range lines {
		var value any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("redacted JSONL is invalid: %v\n%s", err, line)
		}
	}
	output := string(RedactJSONL(input))
	if strings.Contains(output, "line-secret") || strings.Contains(output, "invalid-secret") {
		t.Fatalf("JSONL redaction leaked a secret: %s", output)
	}
}
