package conflicts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxConflictNameBytes leaves room below the common NAME_MAX=255 limit. The
// value and truncation rules match the Python client's conflict naming.
const MaxConflictNameBytes = 240

var conflictRE = regexp.MustCompile(`^(.*) \(conflitto da ([^ )]+) ([^)]+)\)(\.[^/]*)?$`)

type conflictNameParts struct {
	stem   string
	origin string
	token  string
	suffix string
}

func parseConflictName(name string) (conflictNameParts, bool) {
	match := conflictRE.FindStringSubmatch(name)
	if match == nil {
		return conflictNameParts{}, false
	}
	return conflictNameParts{
		stem:   match[1],
		origin: match[2],
		token:  match[3],
		suffix: match[4],
	}, true
}

func originalName(name string) (string, bool) {
	parts, ok := parseConflictName(name)
	if !ok {
		return "", false
	}
	return parts.stem + parts.suffix, true
}

// splitExtension mirrors pathlib.Path.stem/suffix, including dotfiles,
// extensionless files and names with a trailing dot.
func splitExtension(name string) (stem, suffix string) {
	stem = name
	if strings.Trim(name, ".") == "" {
		return name, ""
	}
	index := strings.LastIndexByte(name, '.')
	if index <= 0 || strings.Trim(name[:index], ".") == "" {
		return name, ""
	}
	return name[:index], name[index:]
}

func sanitizeOrigin(origin string) string {
	origin = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '_', r == '-':
			return r
		case r < 0x20 || r == 0x7f, r == ' ', r == ')', r == '(', r == '/', r == '\\':
			return '_'
		default:
			return r
		}
	}, strings.TrimSpace(origin))
	if origin == "" {
		return "sconosciuto"
	}
	return truncateUTF8(origin, 64)
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func conflictToken(originalRelative, digest string) string {
	sum := sha256.Sum256([]byte(originalRelative + "\x00" + digest))
	return hex.EncodeToString(sum[:4])
}

// ConflictName returns a Python-compatible conflict filename for a relative
// original path. Repeated calls with the same path, origin and digest return
// the same name, including for extensionless and multi-extension files.
func ConflictName(originalRelative, originDevice, digest string) (string, error) {
	relative, err := cleanRelative(originalRelative)
	if err != nil {
		return "", err
	}
	base := path.Base(relative)
	stem, suffix := splitExtension(base)
	origin := sanitizeOrigin(originDevice)
	tag := fmt.Sprintf("%s%s %s)", ConflictTag, origin, conflictToken(relative, digest))
	if len(tag+suffix) >= MaxConflictNameBytes {
		suffix = ""
		stem = base
	}
	budget := MaxConflictNameBytes - len(tag+suffix)
	if budget < 0 {
		return "", fmt.Errorf("il nome del dispositivo e' troppo lungo")
	}
	stem = truncateUTF8(stem, budget)
	if stem == "" {
		stem = "file"
	}
	return stem + tag + suffix, nil
}

func conflictRelativeName(originalRelative, originDevice, digest string) (string, error) {
	relative, err := cleanRelative(originalRelative)
	if err != nil {
		return "", err
	}
	name, err := ConflictName(relative, originDevice, digest)
	if err != nil {
		return "", err
	}
	return path.Join(path.Dir(relative), name), nil
}
