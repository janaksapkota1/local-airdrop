// Package node runs a complete HomeDrop device — identity, discovery,
// receiving server and sender — and reports everything as events for a UI.
package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/janak/homedrop/internal/discovery"
	"github.com/janak/homedrop/internal/identity"
	"github.com/janak/homedrop/internal/transfer"
)

// Settings are user-changeable and persisted in settings.json.
type Settings struct {
	Name        string `json:"name"`
	DownloadDir string `json:"downloadDir"`
	AutoAccept  bool   `json:"autoAccept"` // from trusted devices only
}

type Config struct {
	DataDir  string
	Port     int    // transfer port (0 = default 53530)
	OS       string // reported OS (default runtime.GOOS; "android" on phones)
	Defaults Settings
}

type Self struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	OS   string `json:"os"`
	Port int    `json:"port"`
}

type PeerView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OS      string `json:"os"`
	Addr    string `json:"addr"`
	Trusted bool   `json:"trusted"`
}

type OfferView struct {
	ID         string    `json:"id"`
	SenderID   string    `json:"senderId"`
	SenderName string    `json:"senderName"`
	SenderOS   string    `json:"senderOs"`
	Trusted    bool      `json:"trusted"`
	PairCode   string    `json:"pairCode"`
	FileCount  int       `json:"fileCount"`
	TotalSize  int64     `json:"totalSize"`
	Roots      []string  `json:"roots"` // top-level names, e.g. ["Photos", "a.pdf"]
	Text       string    `json:"text,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt"`

	decision chan bool
}

// Transfer states.
const (
	StateWaiting   = "waiting" // offer sent, waiting for the receiver
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateDeclined  = "declined"
	StateCancelled = "cancelled"
)

type TransferView struct {
	ID        string    `json:"id"`
	Direction string    `json:"direction"` // "send" | "receive"
	PeerID    string    `json:"peerId"`
	PeerName  string    `json:"peerName"`
	PairCode  string    `json:"pairCode,omitempty"` // shown while waiting, for new devices
	Trusted   bool      `json:"trusted"`
	FileCount int       `json:"fileCount"`
	Roots     []string  `json:"roots"`
	Total     int64     `json:"total"`
	Done      int64     `json:"done"`
	Speed     int64     `json:"speed"` // bytes/s, smoothed
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	Text      string    `json:"text,omitempty"`
	Dir       string    `json:"dir,omitempty"` // where received files went
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`

	cancel    func()
	lastEmit  time.Time
	lastBytes int64
	lastTick  time.Time
}

type Event struct {
	Type string `json:"type"` // peers | offer | offer_closed | transfer | text | settings
	Data any    `json:"data"`
}

type Node struct {
	cfg    Config
	id     *identity.Identity
	trust  *identity.TrustStore
	disc   *discovery.Service
	server *transfer.Server
	client *transfer.Client
	port   int

	mu        sync.Mutex
	settings  Settings
	offers    map[string]*OfferView
	transfers map[string]*TransferView
	bySession map[string]*TransferView
	subs      map[chan Event]struct{}
	chats     map[string]*Conversation // peer ID → chat
}

// Start brings the device online. It runs until ctx is cancelled.
func Start(ctx context.Context, cfg Config) (*Node, error) {
	if cfg.Port == 0 {
		cfg.Port = 53530
	}
	if cfg.OS == "" {
		cfg.OS = runtime.GOOS
	}
	n := &Node{
		cfg: cfg, offers: map[string]*OfferView{}, transfers: map[string]*TransferView{},
		bySession: map[string]*TransferView{}, subs: map[chan Event]struct{}{},
	}
	n.settings = loadSettings(cfg.DataDir, cfg.Defaults)
	n.chats = loadChats(cfg.DataDir)
	if err := os.MkdirAll(n.settings.DownloadDir, 0o755); err != nil {
		return nil, fmt.Errorf("download folder: %w", err)
	}

	var err error
	if n.id, err = identity.Load(cfg.DataDir, n.settings.Name); err != nil {
		return nil, err
	}
	if n.trust, err = identity.LoadTrust(cfg.DataDir); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		// Port busy (e.g. a second instance): take any free port; discovery
		// announces the real one.
		if ln, err = net.Listen("tcp", ":0"); err != nil {
			return nil, err
		}
	}
	n.port = ln.Addr().(*net.TCPAddr).Port

	n.client = transfer.NewClient(n.id, n.trust)
	n.server = transfer.NewServer(n.id, n.trust, n.settings.DownloadDir, n.hooks())
	go func() { _ = n.server.Serve(ctx, ln) }()

	n.disc = discovery.New(discovery.Announce{ID: n.id.Fingerprint, Name: n.settings.Name, OS: cfg.OS, Port: n.port}, true)
	n.disc.OnChange(func() {
		n.emit(Event{"peers", n.Peers()})
		go n.flushQueued() // a device came back: send what was waiting for it
	})
	if err := n.disc.Start(ctx); err != nil {
		// Still usable via IP address, so don't fail.
		fmt.Fprintln(os.Stderr, "discovery unavailable:", err)
	}
	return n, nil
}

func (n *Node) Self() Self {
	n.mu.Lock()
	defer n.mu.Unlock()
	return Self{ID: n.id.Fingerprint, Name: n.settings.Name, OS: n.cfg.OS, Port: n.port}
}

func (n *Node) Peers() []PeerView {
	ps := n.disc.Peers()
	out := make([]PeerView, len(ps))
	for i, p := range ps {
		out[i] = PeerView{ID: p.ID, Name: p.Name, OS: p.OS, Addr: p.Addr, Trusted: n.trust.Trusted(p.ID)}
	}
	return out
}

// Refresh asks all devices to announce themselves now.
func (n *Node) Refresh() { n.disc.Query() }

// ------------------------------------------------------------- events

// Subscribe returns a channel of events; call the returned func to stop.
func (n *Node) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	n.mu.Lock()
	n.subs[ch] = struct{}{}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		delete(n.subs, ch)
		n.mu.Unlock()
	}
}

func (n *Node) emit(e Event) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.emitLocked(e)
}

func (n *Node) emitLocked(e Event) {
	// Marshal now so later mutations don't race with the subscriber.
	raw, _ := json.Marshal(e.Data)
	e.Data = json.RawMessage(raw)
	for ch := range n.subs {
		select {
		case ch <- e:
		default: // slow subscriber; it will resync from State()
		}
	}
}

type State struct {
	Self      Self           `json:"self"`
	Settings  Settings       `json:"settings"`
	Peers     []PeerView     `json:"peers"`
	Offers    []OfferView    `json:"offers"`
	Transfers []TransferView `json:"transfers"`
	Chats     []ChatSummary  `json:"chats"`
}

func (n *Node) State() State {
	peers := n.Peers()
	self := n.Self()
	n.mu.Lock()
	defer n.mu.Unlock()
	online := map[string]bool{}
	for _, p := range peers {
		online[p.ID] = true
	}
	st := State{Self: self, Settings: n.settings, Peers: peers, Chats: n.chatsLocked(online)}
	for _, o := range n.offers {
		st.Offers = append(st.Offers, *o)
	}
	for _, t := range n.transfers {
		st.Transfers = append(st.Transfers, *t)
	}
	sort.Slice(st.Transfers, func(i, j int) bool { return st.Transfers[i].Started.After(st.Transfers[j].Started) })
	return st
}

// ------------------------------------------------------------- settings

func settingsPath(dir string) string { return filepath.Join(dir, "settings.json") }

func loadSettings(dir string, def Settings) Settings {
	if def.Name == "" {
		def.Name, _ = os.Hostname()
	}
	if def.DownloadDir == "" {
		home, _ := os.UserHomeDir()
		def.DownloadDir = filepath.Join(home, "Downloads", "HomeDrop")
	}
	s := def
	if data, err := os.ReadFile(settingsPath(dir)); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.Name == "" {
		s.Name = def.Name
	}
	if s.DownloadDir == "" {
		s.DownloadDir = def.DownloadDir
	}
	return s
}

func (n *Node) UpdateSettings(s Settings) (Settings, error) {
	if s.Name == "" {
		return Settings{}, errors.New("name cannot be empty")
	}
	if !filepath.IsAbs(s.DownloadDir) {
		return Settings{}, errors.New("download folder must be an absolute path")
	}
	if err := os.MkdirAll(s.DownloadDir, 0o755); err != nil {
		return Settings{}, err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(settingsPath(n.cfg.DataDir), data, 0o600); err != nil {
		return Settings{}, err
	}
	n.mu.Lock()
	n.settings = s
	n.id.Name = s.Name
	n.emitLocked(Event{"settings", s})
	n.mu.Unlock()
	n.server.SetDir(s.DownloadDir)
	n.disc.SetName(s.Name)
	return s, nil
}

// ------------------------------------------------------------- receiving

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func roots(files []transfer.Item) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		r := f.Path
		for i := 0; i < len(r); i++ {
			if r[i] == '/' {
				r = r[:i]
				break
			}
		}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

func (n *Node) hooks() transfer.Hooks {
	return transfer.Hooks{
		Decide: func(ctx context.Context, o *transfer.IncomingOffer) bool {
			n.mu.Lock()
			auto := o.Trusted && n.settings.AutoAccept
			n.mu.Unlock()
			if auto {
				return true
			}
			deadline, _ := ctx.Deadline()
			ov := &OfferView{
				ID: newID(), SenderID: o.SenderID, SenderName: o.SenderName, SenderOS: o.SenderOS,
				Trusted: o.Trusted, PairCode: o.PairCode, FileCount: len(o.Files), TotalSize: o.TotalSize,
				Roots: roots(o.Files), Text: o.Text, ExpiresAt: deadline,
				decision: make(chan bool, 1),
			}
			n.mu.Lock()
			n.offers[ov.ID] = ov
			n.emitLocked(Event{"offer", ov})
			n.mu.Unlock()

			var ok bool
			select {
			case ok = <-ov.decision:
			case <-ctx.Done():
			}
			n.mu.Lock()
			delete(n.offers, ov.ID)
			n.emitLocked(Event{"offer_closed", map[string]any{"id": ov.ID, "accepted": ok}})
			n.mu.Unlock()
			return ok
		},
		Text: func(o *transfer.IncomingOffer, text string) {
			n.mu.Lock()
			defer n.mu.Unlock()
			n.receiveOfferTextLocked(o, text)
		},
		Message: n.receiveMessage,
		SessionStart: func(session string, o *transfer.IncomingOffer) {
			n.mu.Lock()
			defer n.mu.Unlock()
			t := &TransferView{ID: newID(), Direction: "receive", PeerID: o.SenderID, PeerName: o.SenderName,
				Trusted: true, FileCount: len(o.Files), Roots: roots(o.Files), Total: o.TotalSize,
				State: StateRunning, Text: o.Text, Dir: n.server.Dir(), Started: time.Now()}
			id := session
			t.cancel = func() { n.server.Cancel(id) }
			n.transfers[t.ID] = t
			n.bySession[session] = t
			n.emitLocked(Event{"transfer", t})
			n.transferToChatLocked(t)
		},
		Progress: func(session string, _ *transfer.IncomingOffer, done, _ int64) {
			n.mu.Lock()
			defer n.mu.Unlock()
			if t := n.bySession[session]; t != nil {
				n.progressLocked(t, done, false)
			}
		},
		SessionDone: func(session string, _ *transfer.IncomingOffer, dir string, err error) {
			n.mu.Lock()
			defer n.mu.Unlock()
			t := n.bySession[session]
			delete(n.bySession, session)
			if t == nil {
				return
			}
			t.Dir = dir
			if err != nil {
				t.State, t.Error = StateCancelled, err.Error()
			} else {
				t.State, t.Done = StateDone, t.Total
			}
			t.Finished, t.Speed = time.Now(), avgSpeed(t)
			n.emitLocked(Event{"transfer", t})
			n.transferStateToChatLocked(t)
		},
	}
}

func avgSpeed(t *TransferView) int64 {
	el := time.Since(t.Started).Seconds()
	if el <= 0 {
		return 0
	}
	return int64(float64(t.Done) / el)
}

// progressLocked updates byte counts and emits at most ~5 events/s.
func (n *Node) progressLocked(t *TransferView, done int64, force bool) {
	now := time.Now()
	t.Done = done
	if dt := now.Sub(t.lastTick).Seconds(); dt >= 0.5 {
		inst := int64(float64(done-t.lastBytes) / dt)
		if t.lastTick.IsZero() || t.Speed == 0 {
			t.Speed = inst
		} else {
			t.Speed = (t.Speed*6 + inst*4) / 10
		}
		t.lastTick, t.lastBytes = now, done
	}
	if force || now.Sub(t.lastEmit) >= 200*time.Millisecond {
		t.lastEmit = now
		n.emitLocked(Event{"transfer", t})
	}
}

// Decide answers a pending incoming offer.
func (n *Node) Decide(offerID string, accept bool) error {
	n.mu.Lock()
	ov := n.offers[offerID]
	n.mu.Unlock()
	if ov == nil {
		return errors.New("offer no longer pending")
	}
	select {
	case ov.decision <- accept:
	default:
	}
	return nil
}

// ------------------------------------------------------------- sending

// SendRequest identifies the target by discovered ID, or by address.
type SendRequest struct {
	To    string   `json:"to"`   // peer ID
	Addr  string   `json:"addr"` // optional ip:port (manual connection)
	Paths []string `json:"paths"`
	Text  string   `json:"text"`
}

func (n *Node) Send(req SendRequest) (string, error) {
	if len(req.Paths) == 0 && req.Text == "" {
		return "", errors.New("nothing to send")
	}
	var target transfer.Target
	if req.Addr != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		t, err := n.client.Resolve(ctx, req.Addr)
		if err != nil {
			return "", fmt.Errorf("cannot reach %s: %w", req.Addr, err)
		}
		target = t
	} else {
		for _, p := range n.disc.Peers() {
			if p.ID == req.To {
				target = transfer.Target{ID: p.ID, Name: p.Name, Addr: p.Addr}
			}
		}
		if target.ID == "" {
			return "", errors.New("device is no longer visible")
		}
	}

	var items []transfer.Item
	var total int64
	if len(req.Paths) > 0 {
		m, err := transfer.BuildManifest(req.Paths)
		if err != nil {
			return "", err
		}
		for _, f := range m {
			items = append(items, f.Item)
			total += f.Size
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &TransferView{
		ID: newID(), Direction: "send", PeerID: target.ID, PeerName: target.Name,
		FileCount: len(items), Roots: roots(items), Total: total, State: StateWaiting,
		Text: req.Text, Started: time.Now(), cancel: cancel,
	}
	n.mu.Lock()
	n.transfers[t.ID] = t
	n.emitLocked(Event{"transfer", t})
	n.transferToChatLocked(t)
	n.mu.Unlock()

	go func() {
		defer cancel()
		err := n.client.Send(ctx, target, req.Paths, req.Text, transfer.SendOptions{
			OnOffer: func(code string, trusted bool) {
				n.mu.Lock()
				t.PairCode, t.Trusted = code, trusted
				n.emitLocked(Event{"transfer", t})
				n.mu.Unlock()
			},
			OnAccepted: func() {
				n.mu.Lock()
				t.State, t.Trusted, t.Started = StateRunning, true, time.Now()
				n.emitLocked(Event{"transfer", t})
				n.mu.Unlock()
			},
			Progress: func(done, _ int64) {
				n.mu.Lock()
				n.progressLocked(t, done, false)
				n.mu.Unlock()
			},
		})
		n.mu.Lock()
		defer n.mu.Unlock()
		t.Finished = time.Now()
		switch {
		case err == nil:
			t.State, t.Done = StateDone, t.Total
		case errors.Is(err, transfer.ErrDeclined):
			t.State, t.Error = StateDeclined, err.Error()
		case errors.Is(err, context.Canceled):
			t.State, t.Error = StateCancelled, "cancelled"
		case errors.Is(err, transfer.ErrCancelled):
			t.State, t.Error = StateCancelled, err.Error()
		default:
			t.State, t.Error = StateFailed, err.Error()
		}
		t.Speed = avgSpeed(t)
		n.emitLocked(Event{"transfer", t})
		n.transferStateToChatLocked(t)
	}()
	return t.ID, nil
}

// Cancel stops a running transfer in either direction.
func (n *Node) Cancel(transferID string) error {
	n.mu.Lock()
	t := n.transfers[transferID]
	var cancel func()
	if t != nil && (t.State == StateRunning || t.State == StateWaiting) {
		cancel = t.cancel
	}
	n.mu.Unlock()
	if cancel == nil {
		return errors.New("transfer is not active")
	}
	cancel()
	return nil
}

// ClearFinished removes finished transfers from the list.
func (n *Node) ClearFinished() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, t := range n.transfers {
		if t.State != StateRunning && t.State != StateWaiting {
			delete(n.transfers, id)
		}
	}
	n.emitLocked(Event{"cleared", nil})
}
