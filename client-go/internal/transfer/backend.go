package transfer

import (
	"strings"

	"nasbox-go/internal/config"
)

// EffectiveBackend returns the backend the client will actually use. The
// bootstrap stores the negotiated value in effective_transfer_backend; before
// the first bootstrap, fall back to the configured preference. The default is
// the historical rsync path, matching remote.Bootstrap.
func EffectiveBackend(cfg *config.Config) string {
	if cfg == nil {
		return "rsync"
	}
	backend := strings.ToLower(strings.TrimSpace(cfg.String("effective_transfer_backend")))
	if backend == "" {
		backend = strings.ToLower(strings.TrimSpace(cfg.String("transfer_backend")))
	}
	switch backend {
	case "chunks", "rsync":
		return backend
	case "":
		return "rsync"
	default:
		return backend
	}
}
