package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/janak/homedrop/internal/discovery"
	"github.com/janak/homedrop/internal/transfer"
)

// Devices added by IP address are remembered so they show up (and count as
// online) on networks where multicast discovery cannot reach them, e.g. a
// wired PC and a phone on Wi-Fi in different VLANs.

const (
	probeInterval = 5 * time.Second
	// A saved device not heard from (by UDP) for this long is checked over TCP.
	probeAfter   = 7 * time.Second
	probeTimeout = 3 * time.Second
)

// SavedAddr is a device that was reached by IP address.
type SavedAddr struct {
	Addr string `json:"addr"` // host:port of its transfer server
	ID   string `json:"id"`
	Name string `json:"name"`
	OS   string `json:"os"`
}

func savedPath(dir string) string { return filepath.Join(dir, "addresses.json") }

func loadSaved(dir string) []SavedAddr {
	var out []SavedAddr
	if data, err := os.ReadFile(savedPath(dir)); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func (n *Node) persistSavedLocked() {
	data, _ := json.MarshalIndent(n.saved, "", "  ")
	_ = os.WriteFile(savedPath(n.cfg.DataDir), data, 0o600)
	n.emitLocked(Event{"saved", append([]SavedAddr{}, n.saved...)})
}

// NormalizeAddr turns "10.0.0.5" into "10.0.0.5:53530".
func NormalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(strings.Trim(addr, "[]"), "53530")
	}
	return addr
}

// AddAddress connects to a device by address and remembers it.
func (n *Node) AddAddress(addr string) (PeerView, error) {
	addr = NormalizeAddr(addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t, err := n.client.Resolve(ctx, addr)
	if err != nil {
		return PeerView{}, fmt.Errorf("cannot reach %s: %w", addr, err)
	}
	if t.ID == n.id.Fingerprint {
		return PeerView{}, errors.New("that address is this device")
	}
	n.remember(t)
	return PeerView{ID: t.ID, Name: t.Name, OS: t.OS, Addr: t.Addr, Trusted: n.trust.Trusted(t.ID)}, nil
}

// RemoveAddress forgets a saved address.
func (n *Node) RemoveAddress(addr string) {
	n.mu.Lock()
	kept := n.saved[:0]
	for _, s := range n.saved {
		if s.Addr != addr {
			kept = append(kept, s)
		}
	}
	changed := len(kept) != len(n.saved)
	n.saved = kept
	if changed {
		n.persistSavedLocked()
	}
	n.mu.Unlock()
	n.disc.SetDirect(n.directHosts())
}

// remember saves (or updates) a device that answered at t.Addr and marks it
// as seen. A device whose IP changed keeps one entry, matched by ID.
func (n *Node) remember(t transfer.Target) {
	entry := SavedAddr{Addr: t.Addr, ID: t.ID, Name: t.Name, OS: t.OS}
	n.mu.Lock()
	changed := true
	found := false
	kept := n.saved[:0]
	for _, s := range n.saved {
		if s.ID == t.ID || s.Addr == t.Addr {
			if !found {
				changed = s != entry
				kept = append(kept, entry)
				found = true
			}
			continue // drop duplicates (old address of this device, or another device's old entry at this address)
		}
		kept = append(kept, s)
	}
	if !found {
		kept = append(kept, entry)
	} else if len(kept) != len(n.saved) {
		changed = true
	}
	n.saved = kept
	if changed {
		n.persistSavedLocked()
	}
	n.mu.Unlock()
	if changed {
		n.disc.SetDirect(n.directHosts())
	}
	n.disc.Seen(discovery.Peer{ID: t.ID, Name: t.Name, OS: t.OS, Addr: t.Addr})
}

// directHosts lists the hosts of saved devices, for direct UDP queries.
func (n *Node) directHosts() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, s := range n.saved {
		if h, _, err := net.SplitHostPort(s.Addr); err == nil {
			out = append(out, h)
		}
	}
	return out
}

// probeSaved keeps saved devices online over TCP when UDP doesn't get
// through (firewall) or the other device is an older version.
func (n *Node) probeSaved(ctx context.Context) {
	t := time.NewTicker(probeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		fresh := map[string]bool{}
		for _, p := range n.disc.Peers() {
			if time.Since(p.LastSeen) < probeAfter {
				fresh[p.ID] = true
			}
		}
		n.mu.Lock()
		saved := append([]SavedAddr{}, n.saved...)
		n.mu.Unlock()

		var wg sync.WaitGroup
		for _, s := range saved {
			if fresh[s.ID] {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				pctx, cancel := context.WithTimeout(ctx, probeTimeout)
				defer cancel()
				tg, err := n.client.Resolve(pctx, s.Addr)
				if err != nil || tg.ID == n.id.Fingerprint {
					return
				}
				n.remember(tg) // updates the entry only if something changed
			}()
		}
		wg.Wait()
	}
}
