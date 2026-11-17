package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSendMany(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b, c := startNode(t, ctx, "alice"), startNode(t, ctx, "bob"), startNode(t, ctx, "carol")
	autoAccept(b)
	autoAccept(c)
	bid, cid := b.Self().ID, c.Self().ID
	waitFor(t, "discovery", func() bool { _, ok := a.peer(bid); _, ok2 := a.peer(cid); return ok && ok2 })

	src := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(src, []byte("hello everyone"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := a.SendMany(SendManyRequest{All: true}); err == nil {
		t.Fatal("expected error for empty send")
	}
	if _, err := a.SendMany(SendManyRequest{To: []string{"nope"}, Paths: []string{src}}); err == nil {
		t.Fatal("expected error when no device can be reached")
	}

	res, err := a.SendMany(SendManyRequest{All: true, Paths: []string{src}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IDs[bid] == "" || res.IDs[cid] == "" {
		t.Fatalf("expected a transfer per device, got %+v", res)
	}
	done := func(id string) bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.transfers[id].State == StateDone
	}
	waitFor(t, "both transfers", func() bool { return done(res.IDs[bid]) && done(res.IDs[cid]) })

	for _, n := range []*Node{b, c} {
		got, err := os.ReadFile(filepath.Join(n.server.Dir(), "hello.txt"))
		if err != nil || string(got) != "hello everyone" {
			t.Fatalf("%s: got %q, %v", n.Self().Name, got, err)
		}
	}
}
