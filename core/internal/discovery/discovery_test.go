package discovery

import (
	"context"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"
)

func has(s *Service, id string) bool {
	for _, p := range s.Peers() {
		if p.ID == id {
			return true
		}
	}
	return false
}

// Two devices whose multicast can't reach each other (different ports here,
// different VLANs in real life). Only A knows B's address, yet both end up
// seeing each other through direct queries and replies.
func TestDirectDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pa, pb := 40000+rand.IntN(10000), 50000+rand.IntN(10000)
	a := New(Announce{ID: "aaaa", Name: "A", OS: "linux", Port: 1111}, true).WithPort(pa)
	b := New(Announce{ID: "bbbb", Name: "B", OS: "android", Port: 2222}, true).WithPort(pb)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	a.SetDirect([]string{"127.0.0.1:" + strconv.Itoa(pb)})

	deadline := time.Now().Add(3 * time.Second)
	for !(has(a, "bbbb") && has(b, "aaaa")) {
		if time.Now().After(deadline) {
			t.Fatalf("not discovered: a sees b=%v, b sees a=%v", has(a, "bbbb"), has(b, "aaaa"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, p := range a.Peers() {
		if p.ID == "bbbb" && p.Addr != "127.0.0.1:2222" {
			t.Fatalf("wrong addr %s", p.Addr)
		}
	}
}
