package node

import (
	"context"
	"math/rand/v2"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func startIsolated(t *testing.T, ctx context.Context, name string) *Node {
	t.Helper()
	dir := t.TempDir()
	n, err := Start(ctx, Config{
		DataDir:       dir,
		Port:          40000 + rand.IntN(20000),
		DiscoveryPort: 30000 + rand.IntN(9000), // own port: multicast can't find the other node
		Defaults:      Settings{Name: name, DownloadDir: filepath.Join(dir, "dl")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A device added by IP shows as online, stays online while reachable even
// without discovery packets (TCP fallback), and is remembered across restarts.
func TestSavedAddressStaysOnline(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past the peer TTL")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := startIsolated(t, ctx, "alice")
	b := startIsolated(t, ctx, "bob")
	bid := b.Self().ID

	if _, err := a.AddAddress("127.0.0.1:" + strconv.Itoa(b.Self().Port)); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.peer(bid); !ok {
		t.Fatal("bob not online right after adding")
	}
	if _, err := a.AddAddress("127.0.0.1:" + strconv.Itoa(a.Self().Port)); err == nil {
		t.Fatal("adding own address should fail")
	}

	// Past the 16 s TTL: only the TCP probe keeps bob listed.
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(time.Second) {
		if _, ok := a.peer(bid); !ok {
			t.Fatal("bob went offline although reachable")
		}
	}
	if s := loadSaved(a.cfg.DataDir); len(s) != 1 || s[0].ID != bid {
		t.Fatalf("saved = %+v", s)
	}

	a.RemoveAddress(a.State().Saved[0].Addr)
	if len(loadSaved(a.cfg.DataDir)) != 0 {
		t.Fatal("address not removed")
	}
}
