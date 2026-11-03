package transfer

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/janak/homedrop/internal/identity"
)

func TestSafeSegments(t *testing.T) {
	good := []string{"a.txt", "Photos/2024/img 1.jpg", "ünïcode/文件.txt", ".hidden"}
	bad := []string{"", "/etc/passwd", "../x", "a/../../x", "a//b", "a/./b", "a\\b", "C:\\x",
		"a\x00b", "a/\nb", "x" + partSuffix, strings.Repeat("a", 256)}
	for _, p := range good {
		if _, err := SafeSegments(p); err != nil {
			t.Errorf("%q should be allowed: %v", p, err)
		}
	}
	for _, p := range bad {
		if _, err := SafeSegments(p); err == nil {
			t.Errorf("%q should be rejected", p)
		}
	}
}

type node struct {
	id     *identity.Identity
	trust  *identity.TrustStore
	dir    string
	addr   string
	client *Client

	mu     sync.Mutex
	texts  []string
	done   chan error
	accept bool
}

func newNode(t *testing.T, name string, accept bool) *node {
	t.Helper()
	data := t.TempDir()
	id, err := identity.Load(data, name)
	if err != nil {
		t.Fatal(err)
	}
	trust, _ := identity.LoadTrust(data)
	n := &node{id: id, trust: trust, dir: t.TempDir(), accept: accept, done: make(chan error, 10)}
	n.client = NewClient(id, trust)

	srv := NewServer(id, trust, n.dir, Hooks{
		Decide: func(ctx context.Context, o *IncomingOffer) bool { return n.accept },
		Text: func(o *IncomingOffer, text string) {
			n.mu.Lock()
			n.texts = append(n.texts, text)
			n.mu.Unlock()
		},
		SessionDone: func(_ string, _ *IncomingOffer, _ string, err error) { n.done <- err },
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Serve(ctx, ln)
	n.addr = ln.Addr().String()
	return n
}

func (n *node) target() Target { return Target{ID: n.id.Fingerprint, Name: n.id.Name, Addr: n.addr} }

func writeRandom(t *testing.T, path string, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	_, _ = rand.Read(data)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

func expectFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: content mismatch (got %d bytes, want %d)", path, len(got), len(want))
	}
}

func waitDone(t *testing.T, n *node) {
	t.Helper()
	select {
	case err := <-n.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("receiver never finished")
	}
}

func TestSendFolderAndText(t *testing.T) {
	sender, recv := newNode(t, "laptop", true), newNode(t, "phone", true)
	src := t.TempDir()
	big := writeRandom(t, filepath.Join(src, "Photos", "big.bin"), 20<<20)
	small := writeRandom(t, filepath.Join(src, "Photos", "sub", "small.txt"), 1234)
	empty := writeRandom(t, filepath.Join(src, "Photos", "empty"), 0)
	single := writeRandom(t, filepath.Join(src, "notes.md"), 5000)

	err := sender.client.Send(context.Background(), recv.target(),
		[]string{filepath.Join(src, "Photos"), filepath.Join(src, "notes.md")}, "hello 👋", SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, recv)
	expectFile(t, filepath.Join(recv.dir, "Photos", "big.bin"), big)
	expectFile(t, filepath.Join(recv.dir, "Photos", "sub", "small.txt"), small)
	expectFile(t, filepath.Join(recv.dir, "Photos", "empty"), empty)
	expectFile(t, filepath.Join(recv.dir, "notes.md"), single)
	if len(recv.texts) != 1 || recv.texts[0] != "hello 👋" {
		t.Fatalf("text not delivered: %q", recv.texts)
	}
	if !recv.trust.Trusted(sender.id.Fingerprint) || !sender.trust.Trusted(recv.id.Fingerprint) {
		t.Fatal("devices should trust each other after an accepted transfer")
	}

	// Sending again must not overwrite: we expect "notes (1).md".
	if err := sender.client.Send(context.Background(), recv.target(), []string{filepath.Join(src, "notes.md")}, "", SendOptions{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, recv)
	expectFile(t, filepath.Join(recv.dir, "notes (1).md"), single)
}

// One big file must be split across parallel connections and still be
// reassembled byte-for-byte, including an uneven last chunk.
func TestSingleFileParallelChunks(t *testing.T) {
	sender, recv := newNode(t, "a", true), newNode(t, "b", true)
	sender.client.chunkSize = 256 << 10
	f := filepath.Join(t.TempDir(), "big.bin")
	data := writeRandom(t, f, 5<<20+12345) // 21 chunks, last one partial
	if err := sender.client.Send(context.Background(), recv.target(), []string{f}, "", SendOptions{Parallel: 4}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, recv)
	expectFile(t, filepath.Join(recv.dir, "big.bin"), data)
	if left, _ := filepath.Glob(filepath.Join(recv.dir, "*"+partSuffix)); len(left) > 0 {
		t.Fatalf("leftover part files: %v", left)
	}
}

func TestDecline(t *testing.T) {
	sender, recv := newNode(t, "a", true), newNode(t, "b", false)
	f := filepath.Join(t.TempDir(), "x.txt")
	writeRandom(t, f, 10)
	err := sender.client.Send(context.Background(), recv.target(), []string{f}, "", SendOptions{})
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("want ErrDeclined, got %v", err)
	}
	if sender.trust.Trusted(recv.id.Fingerprint) {
		t.Fatal("declined device must not become trusted")
	}
}

func TestResumeAfterNetworkFailure(t *testing.T) {
	sender, recv := newNode(t, "a", true), newNode(t, "b", true)
	f := filepath.Join(t.TempDir(), "movie.mkv")
	data := writeRandom(t, f, 16<<20)
	sender.client.chunkSize = 1 << 20        // 16 chunks
	sender.client.failAfter.Store(512 << 10) // drop the first connection half-way through a chunk

	var mu sync.Mutex
	var maxDone int64
	err := sender.client.Send(context.Background(), recv.target(), []string{f}, "", SendOptions{
		Progress: func(done, total int64) {
			if done > total {
				t.Errorf("progress %d > total %d", done, total)
			}
			mu.Lock()
			maxDone = max(maxDone, done)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, recv)
	expectFile(t, filepath.Join(recv.dir, "movie.mkv"), data)
	// Only the broken chunk is re-sent: at most one extra chunk on the wire.
	if sent := sender.client.wireBytes.Load(); sent > int64(len(data))+1<<20 {
		t.Fatalf("sent %d bytes for a %d byte file: more than the failed chunk was re-sent", sent, len(data))
	}
	if maxDone != int64(len(data)) {
		t.Fatalf("final progress %d, want %d", maxDone, len(data))
	}
}

func TestWrongFingerprintRejected(t *testing.T) {
	sender, recv, other := newNode(t, "a", true), newNode(t, "b", true), newNode(t, "c", true)
	f := filepath.Join(t.TempDir(), "x.txt")
	writeRandom(t, f, 10)
	tgt := recv.target()
	tgt.ID = other.id.Fingerprint // discovery said "c" but "b" answers → must refuse
	if err := sender.client.Send(context.Background(), tgt, []string{f}, "", SendOptions{}); err == nil {
		t.Fatal("expected TLS pinning failure")
	}
}

func TestPathTraversalRejected(t *testing.T) {
	sender, recv := newNode(t, "a", true), newNode(t, "b", true)
	hc := sender.client.httpClient(recv.id.Fingerprint, 1)
	body := []byte(`{"senderName":"evil","files":[{"id":0,"path":"../../.bashrc","size":3}]}`)
	err := doJSON(context.Background(), hc, "POST", "https://"+recv.addr+"/v1/offer", body, nil)
	var he *httpError
	if !errors.As(err, &he) || he.code != 400 {
		t.Fatalf("want HTTP 400, got %v", err)
	}
}

func TestResolveByAddress(t *testing.T) {
	sender, recv := newNode(t, "a", true), newNode(t, "b", true)
	tgt, err := sender.client.Resolve(context.Background(), recv.addr)
	if err != nil {
		t.Fatal(err)
	}
	if tgt.ID != recv.id.Fingerprint || tgt.Name != "b" {
		t.Fatalf("bad resolve result: %+v", tgt)
	}
}
