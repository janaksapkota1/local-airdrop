package node

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func startNode(t *testing.T, ctx context.Context, name string) *Node {
	t.Helper()
	dir := t.TempDir()
	n, err := Start(ctx, Config{
		DataDir:  dir,
		Port:     40000 + rand.IntN(20000),
		Defaults: Settings{Name: name, DownloadDir: filepath.Join(dir, "dl")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// autoAccept answers every incoming offer on n with yes.
func autoAccept(n *Node) {
	ch, _ := n.Subscribe()
	go func() {
		for e := range ch {
			if e.Type == "offer" {
				var o OfferView
				_ = json.Unmarshal(e.Data.(json.RawMessage), &o)
				_ = n.Decide(o.ID, true)
			}
		}
	}()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func lastMsg(n *Node, peer string) ChatMessage {
	ms := n.Messages(peer)
	if len(ms) == 0 {
		return ChatMessage{}
	}
	return ms[len(ms)-1]
}

func TestChat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := startNode(t, ctx, "alice"), startNode(t, ctx, "bob")
	autoAccept(b)
	aid, bid := a.Self().ID, b.Self().ID
	waitFor(t, "discovery", func() bool { _, ok := a.peer(bid); _, ok2 := b.peer(aid); return ok && ok2 })

	// 1. Not paired yet: goes through the offer/accept flow, then pairs.
	if err := a.SendChat(bid, "hi bob"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first message", func() bool { return lastMsg(b, aid).Text == "hi bob" })
	waitFor(t, "sent status", func() bool { return lastMsg(a, bid).Status == MsgSent })
	if !a.trust.Trusted(bid) || !b.trust.Trusted(aid) {
		t.Fatal("devices should be paired after the first accepted message")
	}

	// 2. Paired: direct delivery both ways, no prompt.
	if err := a.SendChat(bid, "second"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "direct message", func() bool { return lastMsg(b, aid).Text == "second" })
	if err := b.SendChat(aid, "hello alice"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reply", func() bool { return lastMsg(a, bid).Text == "hello alice" })
	waitFor(t, "reply sent", func() bool { return lastMsg(b, aid).Status == MsgSent })

	// Unread counts and mark-read.
	if s := b.Chats(); len(s) != 1 || s[0].Unread != 2 || !s[0].Online {
		t.Fatalf("bob's chat summary wrong: %+v", s)
	}
	b.MarkRead(aid)
	if b.Chats()[0].Unread != 0 {
		t.Fatal("mark read failed")
	}

	// 3. A file transfer shows up in both chats.
	f := filepath.Join(t.TempDir(), "photo.jpg")
	_ = os.WriteFile(f, []byte("jpeg bytes"), 0o644)
	if _, err := a.Send(SendRequest{To: bid, Paths: []string{f}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "file in sender chat", func() bool {
		m := lastMsg(a, bid)
		return m.Kind == "files" && m.Status == MsgSent && m.Files.State == StateDone
	})
	waitFor(t, "file in receiver chat", func() bool {
		m := lastMsg(b, aid)
		return m.Kind == "files" && m.Files.State == StateDone && m.Files.Roots[0] == "photo.jpg"
	})

	// 4. History is saved.
	saved := loadChats(a.cfg.DataDir)
	if c := saved[bid]; c == nil || len(c.Messages) != 4 || c.PeerName != "bob" {
		t.Fatalf("history not persisted correctly: %+v", c)
	}
}

func TestChatQueuedUntilOnline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := startNode(t, ctx, "alice")
	bctx, bstop := context.WithCancel(ctx)
	b := startNode(t, bctx, "bob")
	autoAccept(b)
	aid, bid := a.Self().ID, b.Self().ID
	waitFor(t, "discovery", func() bool { _, ok := a.peer(bid); return ok })
	if err := a.SendChat(bid, "pair"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "paired", func() bool { return lastMsg(a, bid).Status == MsgSent })

	// Bob goes offline (sends "bye"), Alice writes anyway.
	bstop()
	waitFor(t, "bob offline", func() bool { _, ok := a.peer(bid); return !ok })
	if err := a.SendChat(bid, "are you there?"); err != nil {
		t.Fatal(err)
	}
	if st := lastMsg(a, bid).Status; st != MsgQueued {
		t.Fatalf("want queued, got %s", st)
	}

	// Bob comes back with the same identity: the message goes out by itself.
	b2, err := Start(ctx, Config{DataDir: b.cfg.DataDir, Port: b.cfg.Port, Defaults: b.cfg.Defaults})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "queued message delivered", func() bool { return lastMsg(b2, aid).Text == "are you there?" })
	waitFor(t, "sent status", func() bool { return lastMsg(a, bid).Status == MsgSent })
}
