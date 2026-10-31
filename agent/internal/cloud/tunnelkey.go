package cloud

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/tunnelseal"
)

// TunnelKeyPair is the cloud's static key of the sealed browser tunnel (§9.10) with its certificate.
type TunnelKeyPair struct {
	Private *ecdh.PrivateKey
	Cert    tunnelseal.KeyCert
}

var (
	tunnelKeyMu    sync.Mutex
	tunnelKeyCache *TunnelKeyPair
	tunnelKeyID    string // what the cached pair was made from
)

func tunnelKeyFile(dataDir string) string {
	return filepath.Join(dataDir, "secrets", "tunnel-x25519.key")
}

// releaseSeed reads the release private key: OPM_RELEASE_PRIVATE_KEY, else the data dir's key file. Unlike releaseKey it
// never creates one: a new release key would not be the one the agents have pinned. "" = none here.
func releaseSeed(dataDir string) string {
	if s := strings.TrimSpace(os.Getenv("OPM_RELEASE_PRIVATE_KEY")); s != "" {
		return s
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "secrets", "release-key.json"))
	if err != nil {
		return ""
	}
	m, err := doc.ParseMap(raw)
	if err != nil {
		return ""
	}
	return m.Str("privateKeySeedB64")
}

// loadTunnelX25519 reads <dataDir>/secrets/tunnel-x25519.key (base64) or creates it, 0600.
func loadTunnelX25519(dataDir string) (*ecdh.PrivateKey, error) {
	f := tunnelKeyFile(dataDir)
	curve := ecdh.X25519()
	for try := 0; try < 2; try++ {
		raw, err := os.ReadFile(f)
		if err == nil {
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
			if err != nil {
				return nil, errors.New("tunnel-x25519.key is not base64")
			}
			return curve.NewPrivateKey(b)
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		k, err := curve.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			return nil, err
		}
		out, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue // another process won: read its key
		}
		if err != nil {
			return nil, err
		}
		_, err = out.WriteString(base64.StdEncoding.EncodeToString(k.Bytes()) + "\n")
		if e := out.Close(); err == nil {
			err = e
		}
		if err != nil {
			_ = os.Remove(f)
			return nil, err
		}
		driver.Log.Info("tunnel key created", "kid", tunnelseal.Kid(k.PublicKey().Bytes()))
		return k, nil
	}
	return nil, errors.New("tunnel-x25519.key: cannot read or create it")
}

// TunnelKey returns the key + certificate of this cloud; without a release private key (or a public URL) the error is
// not-available and sealed tunnelling is off for this cloud.
func TunnelKey() (*TunnelKeyPair, error) {
	dataDir := os.Getenv("OPM_DATA_DIR")
	unavailable := func(why string) error {
		return E.New("not-available", "this cloud has no tunnel key: "+why, nil, nil)
	}
	seedB64 := releaseSeed(dataDir)
	if seedB64 == "" {
		return nil, unavailable("no release private key")
	}
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, unavailable("the release private key is not a base64 Ed25519 seed")
	}
	origin, err := tunnelseal.Origin(Env.APIBase())
	if err != nil {
		return nil, unavailable("no public URL")
	}
	id := dataDir + "\n" + seedB64 + "\n" + origin
	tunnelKeyMu.Lock()
	defer tunnelKeyMu.Unlock()
	if tunnelKeyCache != nil && tunnelKeyID == id {
		return tunnelKeyCache, nil
	}
	priv, err := loadTunnelX25519(dataDir)
	if err != nil {
		return nil, fmt.Errorf("tunnel key: %w", err)
	}
	tunnelKeyCache = &TunnelKeyPair{Private: priv, Cert: tunnelseal.SignCert(ed25519.NewKeyFromSeed(seed), priv.PublicKey().Bytes(), origin)}
	tunnelKeyID = id
	return tunnelKeyCache, nil
}
