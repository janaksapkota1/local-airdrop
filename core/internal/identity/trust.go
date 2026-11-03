package identity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TrustedPeer is a device whose fingerprint the user has confirmed.
type TrustedPeer struct {
	Name  string    `json:"name"`
	Added time.Time `json:"added"`
}

// TrustStore persists confirmed device fingerprints (trust on first use).
type TrustStore struct {
	mu    sync.Mutex
	path  string
	peers map[string]TrustedPeer
}

func LoadTrust(dir string) (*TrustStore, error) {
	t := &TrustStore{path: filepath.Join(dir, "trusted.json"), peers: map[string]TrustedPeer{}}
	data, err := os.ReadFile(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &t.peers); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *TrustStore) Trusted(fp string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.peers[fp]
	return ok
}

func (t *TrustStore) Add(fp, name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.peers[fp] = TrustedPeer{Name: name, Added: time.Now()}
	data, err := json.MarshalIndent(t.peers, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}
