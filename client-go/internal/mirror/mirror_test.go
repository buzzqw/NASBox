package mirror

import (
	"testing"

	"nasbox-go/internal/config"
)

func TestConfiguredReadsMirrorsFromJson(t *testing.T) {
	cfg := config.New("")
	cfg.Set("mirrors", []any{
		map[string]any{"source": "/home/user/RPG", "dest": "RPG", "enabled": true},
	}, false)
	mirrors := Configured(cfg)
	if len(mirrors) != 1 || mirrors[0].Dest != "RPG" || !mirrors[0].Enabled {
		t.Fatalf("bad mirrors: %+v", mirrors)
	}
}
