package transfer

import (
	"testing"

	"nasbox-go/internal/config"
)

func TestEffectiveBackend(t *testing.T) {
	cases := []struct {
		name      string
		effective string
		requested string
		want      string
	}{
		{name: "negotiated chunks wins", effective: "chunks", requested: "rsync", want: "chunks"},
		{name: "negotiated rsync wins", effective: "rsync", requested: "chunks", want: "rsync"},
		{name: "fallback to preference", effective: "", requested: "rsync", want: "rsync"},
		{name: "default is rsync", effective: "", requested: "", want: "rsync"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.New("")
			if err := cfg.Set("effective_transfer_backend", test.effective, false); err != nil {
				t.Fatal(err)
			}
			if err := cfg.Set("transfer_backend", test.requested, false); err != nil {
				t.Fatal(err)
			}
			if got := EffectiveBackend(cfg); got != test.want {
				t.Fatalf("EffectiveBackend() = %q, want %q", got, test.want)
			}
		})
	}
	if got := EffectiveBackend(nil); got != "rsync" {
		t.Fatalf("EffectiveBackend(nil) = %q, want rsync", got)
	}
}
