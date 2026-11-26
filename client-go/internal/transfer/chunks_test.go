package transfer

import (
	"testing"

	"nasbox-go/internal/config"
	"nasbox-go/internal/remote"
)

func TestAutoBackendFollowsNegotiatedCapability(t *testing.T) {
	cfg := config.New("")
	cfg.Set("transfer_backend", "chunks", false)
	backend := NewForConfig(cfg, remote.New(cfg))

	if _, ok := backend.(*autoBackend); !ok {
		t.Fatalf("NewForConfig ha restituito %T, voglio autoBackend", backend)
	}
	if _, ok := backend.(*autoBackend).selected().(*Transfer); !ok {
		t.Fatal("senza capability negoziata il backend deve ripiegare su rsync")
	}
	cfg.Set("effective_transfer_backend", "rsync", false)
	if _, ok := backend.(*autoBackend).selected().(*Transfer); !ok {
		t.Fatal("il backend non è passato a rsync dopo la negoziazione")
	}
	cfg.Set("remote_chunk_transfer_available", true, false)
	cfg.Set("effective_transfer_backend", "chunks", false)
	if _, ok := backend.(*autoBackend).selected().(*Chunks); !ok {
		t.Fatal("il backend non è passato a chunk quando la capability è disponibile")
	}
}
