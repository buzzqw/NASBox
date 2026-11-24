package diagnostics

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

const redacted = "[REDACTED]"

var (
	// This expression handles CLI flags, YAML-style pairs, URL query values and
	// JSON pairs, including quoted values with spaces.
	secretAssignmentRE  = regexp.MustCompile(`(?i)(["']?(?:password|passwd|passphrase|token|secret|credential(?:s)?|api[_-]?key|private[_-]?key|authorization|cookie|session_id)["']?\s*(?:=|:)\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|(?:(?:Bearer|Basic)\s+)?[^\s,;&}"']+)`)
	bearerRE            = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	sshPublicKeyRE      = regexp.MustCompile(`\b(?:ssh-(?:rsa|ed25519)|ecdsa-sha2-\S+|sk-ssh-ed25519)\s+[A-Za-z0-9+/=]+`)
	escapedPrivateKeyRE = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
)

// RedactText removes credentials, bearer tokens, SSH key material and complete
// private-key blocks. It also handles private keys embedded in JSON strings,
// where newlines are represented by the two characters backslash and n.
func RedactText(text string) string {
	text = escapedPrivateKeyRE.ReplaceAllString(text, redacted)
	lines := strings.SplitAfter(text, "\n")
	var result strings.Builder
	result.Grow(len(text))
	inPrivateKey := false
	for _, line := range lines {
		newline := ""
		if strings.HasSuffix(line, "\n") {
			line = strings.TrimSuffix(line, "\n")
			newline = "\n"
		}
		upper := strings.ToUpper(line)
		if inPrivateKey {
			if strings.Contains(upper, "END") && strings.Contains(upper, "PRIVATE KEY") {
				inPrivateKey = false
			}
			result.WriteString(redacted)
			result.WriteString(newline)
			continue
		}
		if strings.Contains(upper, "BEGIN") && strings.Contains(upper, "PRIVATE KEY") {
			if !(strings.Contains(upper, "END") && strings.Contains(upper, "PRIVATE KEY")) {
				inPrivateKey = true
			}
			result.WriteString(redacted)
			result.WriteString(newline)
			continue
		}
		line = secretAssignmentRE.ReplaceAllString(line, "${1}"+redacted)
		line = bearerRE.ReplaceAllString(line, "Bearer "+redacted)
		line = sshPublicKeyRE.ReplaceAllLiteralString(line, redacted)
		result.WriteString(line)
		result.WriteString(newline)
	}
	return result.String()
}

// RedactValue recursively sanitises decoded JSON. Sensitive map keys are
// replaced wholesale, including keys inside mirror/device lists. String values
// also pass through RedactText to catch credentials under an unexpected name.
func RedactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveKey(key) {
				clean[key] = redacted
				continue
			}
			clean[key] = RedactValue(item)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for index, item := range typed {
			clean[index] = RedactValue(item)
		}
		return clean
	case string:
		return RedactText(typed)
	case []byte:
		return RedactText(string(typed))
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	normalised := strings.ToLower(strings.TrimSpace(key))
	normalised = strings.NewReplacer("-", "_").Replace(normalised)
	if normalised == "key" || strings.HasSuffix(normalised, "_key") {
		return true
	}
	for _, token := range []string{
		"password", "passwd", "passphrase", "token", "secret", "credential",
		"private", "known_hosts", "apikey", "authorization", "cookie", "session_id",
	} {
		if strings.Contains(normalised, token) {
			return true
		}
	}
	return false
}

// RedactJSON returns indented, sanitised JSON. Invalid JSON is treated as text
// and redacted rather than copied verbatim, so a broken config cannot leak a
// credential into a support bundle.
func RedactJSON(data []byte) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return []byte("{}\n"), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return []byte(RedactText(string(data))), nil
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return []byte(RedactText(string(data))), nil
	}
	clean, err := json.MarshalIndent(RedactValue(value), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(clean, '\n'), nil
}

// RedactJSONL sanitises a JSON-lines file while keeping one valid JSON object per
// physical line. A damaged line is represented as an invalid_event object rather
// than being silently copied with a possible secret.
func RedactJSONL(data []byte) []byte {
	var result strings.Builder
	result.Grow(len(data))
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimRight(line, "\r\n")
		newline := ""
		if strings.HasSuffix(line, "\n") {
			newline = "\n"
		}
		if strings.TrimSpace(trimmed) == "" {
			if newline != "" {
				result.WriteByte('\n')
			}
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			value = map[string]string{"invalid_event": RedactText(trimmed)}
		}
		encoded, err := json.Marshal(RedactValue(value))
		if err != nil {
			encoded = []byte(`{"invalid_event":"` + redacted + `"}`)
		}
		result.Write(encoded)
		result.WriteString(newline)
	}
	return []byte(result.String())
}
