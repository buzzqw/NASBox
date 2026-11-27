package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"nasbox-go/internal/config"
)

// The update manifest is authenticated with a detached Ed25519 signature:
// manifest-go.json.sig contains the base64 signature over the exact bytes of
// manifest-go.json. The private key never lives on the NAS; only this public
// key is embedded in the client.
const updatePublicKeyHex = "6fd3d5b1066d8a2cb77501b8a514325ec2ad100aa5c1e1d5b77b6cb40f3c9760"

var updatePublicKey = mustUpdatePublicKey(updatePublicKeyHex)

func mustUpdatePublicKey(value string) ed25519.PublicKey {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		panic("chiave pubblica update non valida")
	}
	return ed25519.PublicKey(raw)
}

// ManifestSignaturePath is the detached signature next to the manifest.
func ManifestSignaturePath(cfg *config.Config) string {
	return strings.TrimSuffix(remoteDir(cfg), "/") + "/manifest-go.json.sig"
}

func decodeManifestSignature(raw []byte) ([]byte, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return nil, errors.New("firma update assente")
	}
	signature, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("firma update non valida: %w", err)
	}
	if len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("firma update di dimensione errata: %d", len(signature))
	}
	return signature, nil
}

func verifyManifestSignature(manifest, encoded []byte) error {
	signature, err := decodeManifestSignature(encoded)
	if err != nil {
		return err
	}
	if !ed25519.Verify(updatePublicKey, manifest, signature) {
		return errors.New("firma del manifest update non valida")
	}
	return nil
}
