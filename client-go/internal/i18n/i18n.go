// Package i18n embeds the same translation table as the Python client, so the
// Go client can be developed in parallel without duplicating 1200+ strings.
package i18n

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed strings.json
var raw []byte

var stringsByKey map[string]map[string]string

// Language is the active language code ("it" or "en").
var Language = "it"

func init() {
	_ = json.Unmarshal(raw, &stringsByKey)
}

// T returns a translated string, substituting {name} placeholders from vars.
func T(key string, vars map[string]any) string {
	entry := stringsByKey[key]
	lang := Language
	if _, ok := entry[lang]; !ok {
		lang = "it"
	}
	text, ok := entry[lang]
	if !ok {
		return key
	}
	for name, value := range vars {
		text = strings.ReplaceAll(text, "{"+name+"}", fmt.Sprint(value))
	}
	return text
}
