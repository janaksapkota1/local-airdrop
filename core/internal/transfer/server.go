package transfer

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/janak/homedrop/internal/identity"
)

const (
	decideTimeout = 2 * time.Minute
	sessionIdle   = 5 * time.Minute
	writeBufSize  = 1 << 20
)

var writerPool = sync.Pool{New: func() any { return bufio.NewWriterSize(nil, writeBufSize) }}

// IncomingOffer is what the UI shows the user when someone sends something.
type IncomingOffer struct {
	SenderID   string
	SenderName string
	SenderOS   string
	Trusted    bool   // sender fingerprint was confirmed before
	PairCode   string // compare with the code shown on the sender
	Files      []Item
	TotalSize  int64
	Text       string
}

// Hooks connect the server to a UI. Decide is required; the rest are optional.
type Hooks struct {
	Decide       func(ctx context.Context, o *IncomingOffer) bool
	Text         func(o *IncomingOffer, text string)
	SessionStart func(session string, o *IncomingOffer)                    // files accepted, upload begins
	Progress     func(session string, o *IncomingOffer, done, total int64) // throttled
	SessionDone  func(session string, o *IncomingOffer, dir string, err error)
	Message      func(fromID string, m Message) // chat message from a paired device
}

type Server struct {
	id    *identity.Identity
	trust *identity.TrustStore
	dir   string
	hooks Hooks

	mu       sync.Mutex
	sessions map[string]*session
	reserved map[string]bool // absolute top-level paths claimed by live sessions
}

type session struct {
	id        string
	sender    string
	offer     *IncomingOffer
	files     map[int]*rfile
	roots     []string
	dir       string
	chunkSize int64
	started   time.Time

	done         atomic.Int64
	lastActive   atomic.Int64 // unix nanos
	lastProgress atomic.Int64
	cancelled    atomic.Bool

	mu        sync.Mutex
	remaining int
}

const (
	chunkTodo byte = iota
	chunkBusy
	chunkDone
)

type rfile struct {
	mu         sync.Mutex
	item       Item
	final      string
	part       string
	fh         *os.File
	chunks     []byte // chunkTodo / chunkBusy / chunkDone
	doneChunks int
	finished   bool
}

func NewServer(id *identity.Identity, trust *identity.TrustStore, dir string, hooks Hooks) *Server {
	return &Server{
		id: id, trust: trust, dir: dir, hooks: hooks,
		sessions: map[string]*session{},
		reserved: map[string]bool{},
	}
}

// Serve accepts TLS connections on ln until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", s.handleInfo)
	mux.HandleFunc("POST /v1/offer", s.handleOffer)
	mux.HandleFunc("PUT /v1/upload", s.handleUpload)
	mux.HandleFunc("POST /v1/cancel", s.handleCancel)
	mux.HandleFunc("POST /v1/message", s.handleMessage)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// HTTP/1.1 only: one TCP stream per parallel upload is faster than
		// multiplexing everything over one HTTP/2 connection.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go s.janitor(ctx)

	err := srv.Serve(tls.NewListener(ln, s.id.ServerTLS()))
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func peerFP(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	return identity.Fingerprint(r.TLS.PeerCertificates[0].Raw)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, Info{ID: s.id.Fingerprint, Name: s.id.Name, OS: runtime.GOOS, Version: ProtocolVersion})
}

func (s *Server) handleOffer(w http.ResponseWriter, r *http.Request) {
	fp := peerFP(r)
	if fp == "" {
		writeErr(w, 401, "client certificate required")
		return
	}
	var off Offer
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxManifest)).Decode(&off); err != nil {
		writeErr(w, 400, "bad offer: "+err.Error())
		return
	}
	if len(off.Files) == 0 && off.Text == "" {
		writeErr(w, 400, "empty offer")
		return
	}
	if off.ChunkSize == 0 {
		off.ChunkSize = DefaultChunkSize
	}
	if off.ChunkSize < minChunkSize || off.ChunkSize > maxChunkSize {
		writeErr(w, 400, "bad chunk size")
		return
	}
	if len(off.Files) > maxFiles {
		writeErr(w, 400, "too many files")
		return
	}

	// Validate every path, and make sure no file is also used as a folder.
	var total int64
	ids := map[int]bool{}
	paths := map[string]bool{}
	segs := make(map[int][]string, len(off.Files))
	for _, it := range off.Files {
		sg, err := SafeSegments(it.Path)
		if err != nil || it.Size < 0 || ids[it.ID] || paths[it.Path] {
			writeErr(w, 400, fmt.Sprintf("invalid file entry %q", it.Path))
			return
		}
		ids[it.ID], paths[it.Path], segs[it.ID] = true, true, sg
		total += it.Size
	}
	for _, it := range off.Files {
		sg := segs[it.ID]
		for i := 1; i < len(sg); i++ {
			if paths[strings.Join(sg[:i], "/")] {
				writeErr(w, 400, fmt.Sprintf("%q is both a file and a folder", strings.Join(sg[:i], "/")))
				return
			}
		}
	}
	if free := freeBytes(s.Dir()); free >= 0 && total > free-(64<<20) {
		writeJSON(w, 200, OfferResponse{Reason: "receiver does not have enough free space"})
		return
	}

	in := &IncomingOffer{
		SenderID: fp, SenderName: off.SenderName, SenderOS: off.SenderOS,
		Trusted:  s.trust.Trusted(fp),
		PairCode: identity.PairCode(fp, s.id.Fingerprint),
		Files:    off.Files, TotalSize: total, Text: off.Text,
	}
	ctx, cancel := context.WithTimeout(r.Context(), decideTimeout)
	accepted := s.hooks.Decide != nil && s.hooks.Decide(ctx, in)
	cancel()
	if !accepted {
		writeJSON(w, 200, OfferResponse{Reason: "declined"})
		return
	}
	if !in.Trusted {
		_ = s.trust.Add(fp, off.SenderName)
	}
	if off.Text != "" && s.hooks.Text != nil {
		s.hooks.Text(in, off.Text)
	}
	if len(off.Files) == 0 {
		writeJSON(w, 200, OfferResponse{Accepted: true})
		return
	}

	sess := s.newSession(fp, in, segs, off.ChunkSize)
	if s.hooks.SessionStart != nil {
		s.hooks.SessionStart(sess.id, in)
	}
	writeJSON(w, 200, OfferResponse{Accepted: true, Session: sess.id})
}

// Dir returns the current download directory.
func (s *Server) Dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

// SetDir changes where future transfers are saved.
func (s *Server) SetDir(dir string) {
	s.mu.Lock()
	s.dir = dir
	s.mu.Unlock()
}

// Cancel aborts an incoming session (receiver side) and deletes partial files.
func (s *Server) Cancel(sessionID string) bool {
	s.mu.Lock()
	sess := s.sessions[sessionID]
	s.mu.Unlock()
	if sess == nil {
		return false
	}
	s.finishSession(sess, errors.New("cancelled"))
	return true
}

func (s *Server) newSession(fp string, in *IncomingOffer, segs map[int][]string, chunkSize int64) *session {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	sess := &session{
		id: hex.EncodeToString(buf), sender: fp, offer: in,
		files: make(map[int]*rfile, len(in.Files)), remaining: len(in.Files), started: time.Now(),
		chunkSize: chunkSize,
	}
	sess.lastActive.Store(time.Now().UnixNano())

	s.mu.Lock()
	defer s.mu.Unlock()
	sess.dir = s.dir
	// Pick a free name for each top-level entry: "Photos" → "Photos (1)".
	rootName := map[string]string{}
	for _, it := range in.Files {
		sg := segs[it.ID]
		if _, ok := rootName[sg[0]]; !ok {
			rootName[sg[0]] = s.reserveLocked(sg[0], len(sg) > 1)
			sess.roots = append(sess.roots, filepath.Join(s.dir, rootName[sg[0]]))
		}
		parts := append([]string{s.dir, rootName[sg[0]]}, sg[1:]...)
		final := filepath.Join(parts...)
		n := max(1, int((it.Size+sess.chunkSize-1)/sess.chunkSize))
		sess.files[it.ID] = &rfile{item: it, final: final, part: final + partSuffix, chunks: make([]byte, n)}
	}
	s.sessions[sess.id] = sess
	return sess
}

func (s *Server) reserveLocked(name string, isDir bool) string {
	base, ext := name, ""
	if !isDir {
		ext = filepath.Ext(name)
		base = strings.TrimSuffix(name, ext)
	}
	for i := 0; ; i++ {
		cand := name
		if i > 0 {
			cand = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		full := filepath.Join(s.dir, cand)
		if s.reserved[full] {
			continue
		}
		if _, err := os.Lstat(full); err == nil {
			continue
		}
		if _, err := os.Lstat(full + partSuffix); err == nil {
			continue
		}
		s.reserved[full] = true
		return cand
	}
}

func (s *Server) getSession(r *http.Request) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[r.URL.Query().Get("session")]
	if sess == nil || sess.sender != peerFP(r) {
		return nil
	}
	return sess
}

// hashCounter updates a running CRC32C and progress for every byte read.
type hashCounter struct {
	crc  uint32
	n    int64
	sess *session
	s    *Server
}

var errCancelled = errors.New("session cancelled")

func (h *hashCounter) Write(p []byte) (int, error) {
	if h.sess.cancelled.Load() {
		return 0, errCancelled
	}
	h.crc = crc32.Update(h.crc, castagnoli, p)
	h.n += int64(len(p))
	h.sess.done.Add(int64(len(p)))
	h.s.reportProgress(h.sess)
	return len(p), nil
}

func (s *Server) reportProgress(sess *session) {
	now := time.Now().UnixNano()
	sess.lastActive.Store(now)
	last := sess.lastProgress.Load()
	if s.hooks.Progress != nil && now-last > int64(200*time.Millisecond) && sess.lastProgress.CompareAndSwap(last, now) {
		s.hooks.Progress(sess.id, sess.offer, sess.done.Load(), sess.offer.TotalSize)
	}
}

// handleUpload receives one chunk: PUT /v1/upload?session=&file=&chunk=
// The body is exactly that chunk's bytes; its CRC32C is in the trailer.
// Chunks of one file may arrive in any order and in parallel.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	sess := s.getSession(r)
	if sess == nil {
		writeErr(w, 404, "no such session")
		return
	}
	q := r.URL.Query()
	id, _ := strconv.Atoi(q.Get("file"))
	f := sess.files[id]
	ci, err := strconv.Atoi(q.Get("chunk"))
	if f == nil || err != nil || ci < 0 || ci >= len(f.chunks) {
		writeErr(w, 400, "bad file or chunk")
		return
	}

	f.mu.Lock()
	switch {
	case f.finished || f.chunks[ci] == chunkDone:
		f.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"done": true}) // duplicate after a lost response
		return
	case f.chunks[ci] == chunkBusy:
		f.mu.Unlock()
		writeErr(w, 409, "chunk upload already in progress")
		return
	}
	f.chunks[ci] = chunkBusy
	f.mu.Unlock()

	code, msg, last := s.receiveChunk(sess, f, ci, r)
	if code != 200 {
		f.mu.Lock()
		if f.chunks[ci] == chunkBusy {
			f.chunks[ci] = chunkTodo
		}
		f.mu.Unlock()
		writeErr(w, code, msg)
		return
	}
	writeJSON(w, 200, map[string]bool{"done": true})
	if last {
		s.finishSession(sess, nil)
	}
}

// receiveChunk writes one chunk at its offset. It returns the HTTP status,
// an error message, and whether this completed the whole session.
func (s *Server) receiveChunk(sess *session, f *rfile, ci int, r *http.Request) (int, string, bool) {
	off := int64(ci) * sess.chunkSize
	length := min(sess.chunkSize, f.item.Size-off)
	if sess.cancelled.Load() {
		return 404, "no such session", false
	}

	fh, err := f.open()
	if err != nil {
		return 500, err.Error(), false
	}

	hc := &hashCounter{sess: sess, s: s}
	bw := writerPool.Get().(*bufio.Writer)
	// OffsetWriter has no ReadFrom, so bufio batches TLS's small reads into 1 MB writes.
	bw.Reset(io.NewOffsetWriter(fh, off))
	n, copyErr := bw.ReadFrom(io.TeeReader(io.LimitReader(r.Body, length+1), hc))
	flushErr := bw.Flush()
	bw.Reset(nil)
	writerPool.Put(bw)

	fail := func(code int, msg string) (int, string, bool) {
		sess.done.Add(-hc.n) // these bytes will be sent again
		return code, msg, false
	}
	switch {
	case sess.cancelled.Load():
		f.discard() // in case open() raced with the cancel and recreated it
		return fail(404, "no such session")
	case flushErr != nil:
		return fail(500, "write failed: "+flushErr.Error())
	case n > length:
		return fail(400, "more data than announced")
	case copyErr != nil || n < length:
		return fail(400, "incomplete chunk")
	case r.Trailer.Get(crcTrailer) != fmt.Sprintf("%08x", hc.crc):
		return fail(422, "checksum mismatch")
	}

	f.mu.Lock()
	f.chunks[ci] = chunkDone
	f.doneChunks++
	complete := f.doneChunks == len(f.chunks)
	f.mu.Unlock()
	if !complete {
		return 200, "", false
	}

	if err := f.closeAndPlace(); err != nil {
		return 500, err.Error(), false
	}
	sess.mu.Lock()
	sess.remaining--
	last := sess.remaining == 0
	sess.mu.Unlock()
	return 200, "", last
}

// open returns the shared write handle for the file's .part, creating it
// (at full size, so chunks can land anywhere) on first use.
func (f *rfile) open() (*os.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fh != nil {
		return f.fh, nil
	}
	if err := os.MkdirAll(filepath.Dir(f.part), 0o755); err != nil {
		return nil, err
	}
	fh, err := os.OpenFile(f.part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	if err := fh.Truncate(f.item.Size); err != nil {
		fh.Close()
		return nil, err
	}
	f.fh = fh
	return fh, nil
}

func (f *rfile) closeAndPlace() error {
	f.mu.Lock()
	fh := f.fh
	f.fh = nil
	f.finished = true
	f.mu.Unlock()
	if fh == nil { // zero-length file never opened
		if err := os.MkdirAll(filepath.Dir(f.part), 0o755); err != nil {
			return err
		}
		var err error
		if fh, err = os.Create(f.part); err != nil {
			return err
		}
	}
	if err := fh.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.part, f.final); err != nil {
		return err
	}
	if f.item.Mtime > 0 {
		t := time.Unix(f.item.Mtime, 0)
		_ = os.Chtimes(f.final, t, t)
	}
	return nil
}

func (f *rfile) discard() {
	f.mu.Lock()
	fh := f.fh
	f.fh = nil
	f.mu.Unlock()
	if fh != nil {
		fh.Close()
	}
	_ = os.Remove(f.part)
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	fp := peerFP(r)
	if fp == "" || !s.trust.Trusted(fp) {
		writeErr(w, 403, "not paired")
		return
	}
	var m Message
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMessageText+4096)).Decode(&m); err != nil {
		writeErr(w, 400, "bad message: "+err.Error())
		return
	}
	if m.ID == "" || len(m.ID) > 64 || m.Text == "" || len(m.Text) > maxMessageText {
		writeErr(w, 400, "invalid message")
		return
	}
	if s.hooks.Message != nil {
		s.hooks.Message(fp, m)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	sess := s.getSession(r)
	if sess == nil {
		writeErr(w, 404, "no such session")
		return
	}
	s.finishSession(sess, errors.New("cancelled by sender"))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// finishSession removes a session. On error, partial files are deleted.
func (s *Server) finishSession(sess *session, err error) {
	s.mu.Lock()
	if s.sessions[sess.id] != sess {
		s.mu.Unlock()
		return
	}
	delete(s.sessions, sess.id)
	if err != nil {
		sess.cancelled.Store(true)
	}
	for _, root := range sess.roots {
		delete(s.reserved, root)
	}
	s.mu.Unlock()

	if err != nil {
		for _, f := range sess.files {
			f.discard()
		}
	}
	if s.hooks.SessionDone != nil {
		s.hooks.SessionDone(sess.id, sess.offer, sess.dir, err)
	}
}

// janitor drops sessions the sender abandoned.
func (s *Server) janitor(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var stale []*session
		s.mu.Lock()
		for _, sess := range s.sessions {
			if time.Since(time.Unix(0, sess.lastActive.Load())) > sessionIdle {
				stale = append(stale, sess)
			}
		}
		s.mu.Unlock()
		for _, sess := range stale {
			s.finishSession(sess, errors.New("sender went away"))
		}
	}
}
