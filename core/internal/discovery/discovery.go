// Package discovery finds HomeDrop devices on the local network using UDP
// multicast announcements.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	GroupAddr        = "239.12.34.56:53530"
	announceInterval = 5 * time.Second
	peerTTL          = 16 * time.Second
	minReannounce    = 300 * time.Millisecond
)

// Announce is the packet every device multicasts.
type Announce struct {
	App   string `json:"app"`
	V     int    `json:"v"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	OS    string `json:"os"`
	Port  int    `json:"port"`            // 0 = not receiving (e.g. a CLI sender)
	Query bool   `json:"query,omitempty"` // ask everyone to announce now
	Bye   bool   `json:"bye,omitempty"`   // device is going away
}

// Peer is a device seen on the network.
type Peer struct {
	ID       string
	Name     string
	OS       string
	Addr     string // ip:port of its transfer server
	LastSeen time.Time
}

type Service struct {
	self     Announce
	announce bool // whether we advertise ourselves (receivers do)

	mu       sync.Mutex
	peers    map[string]*Peer
	lastSent time.Time
	send     *net.UDPConn
	onChange func()
}

// New creates a discovery service. If announce is false the device only
// listens and queries (useful for a one-shot sender).
func New(self Announce, announce bool) *Service {
	self.App, self.V = "homedrop", 1
	if !announce {
		self.Port = 0
	}
	return &Service{self: self, announce: announce, peers: map[string]*Peer{}}
}

// SetName changes the advertised device name and re-announces.
func (s *Service) SetName(name string) {
	s.mu.Lock()
	s.self.Name = name
	self := s.self
	s.mu.Unlock()
	if s.announce {
		s.sendPacket(self)
	}
}

// OnChange registers a callback fired when the peer list changes.
func (s *Service) OnChange(fn func()) { s.mu.Lock(); s.onChange = fn; s.mu.Unlock() }

// Start begins listening (and announcing). It stops when ctx is cancelled.
func (s *Service) Start(ctx context.Context) error {
	group, err := net.ResolveUDPAddr("udp4", GroupAddr)
	if err != nil {
		return err
	}
	recv, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		return err
	}
	_ = recv.SetReadBuffer(1 << 20)
	send, err := net.DialUDP("udp4", nil, group)
	if err != nil {
		recv.Close()
		return err
	}
	s.mu.Lock()
	s.send = send
	s.mu.Unlock()

	go s.readLoop(recv)
	go func() {
		t := time.NewTicker(announceInterval)
		defer t.Stop()
		if s.announce {
			s.sendPacket(s.selfAnn())
		}
		s.Query()
		for {
			select {
			case <-ctx.Done():
				if s.announce {
					bye := s.selfAnn()
					bye.Bye = true
					s.sendPacket(bye)
				}
				recv.Close()
				send.Close()
				return
			case <-t.C:
				if s.announce {
					s.sendPacket(s.selfAnn())
				}
				s.expire()
			}
		}
	}()
	return nil
}

// Query asks all devices to announce themselves immediately.
func (s *Service) Query() {
	q := s.selfAnn()
	q.Query = true
	s.sendPacket(q)
}

func (s *Service) sendPacket(a Announce) {
	data, _ := json.Marshal(a)
	s.mu.Lock()
	conn := s.send
	if !a.Query && !a.Bye {
		s.lastSent = time.Now()
	}
	s.mu.Unlock()
	if conn != nil {
		_, _ = conn.Write(data)
	}
}

func (s *Service) readLoop(conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		var a Announce
		if json.Unmarshal(buf[:n], &a) != nil || a.App != "homedrop" || a.ID == "" || a.ID == s.self.ID {
			continue
		}
		s.handle(a, src.IP)
	}
}

func (s *Service) handle(a Announce, ip net.IP) {
	s.mu.Lock()
	changed := false
	isNew := false
	switch {
	case a.Bye:
		if _, ok := s.peers[a.ID]; ok {
			delete(s.peers, a.ID)
			changed = true
		}
	case a.Port > 0:
		addr := net.JoinHostPort(ip.String(), strconv.Itoa(a.Port))
		p, ok := s.peers[a.ID]
		if !ok {
			p = &Peer{ID: a.ID}
			s.peers[a.ID] = p
			isNew = true
		}
		if !ok || p.Addr != addr || p.Name != a.Name {
			changed = true
		}
		p.Name, p.OS, p.Addr, p.LastSeen = a.Name, a.OS, addr, time.Now()
	}
	// Reply quickly to queries and to newcomers so lists fill in fast.
	reply := s.announce && (a.Query || isNew) && time.Since(s.lastSent) > minReannounce
	cb := s.onChange
	s.mu.Unlock()

	if reply {
		s.sendPacket(s.selfAnn())
	}
	if changed && cb != nil {
		cb()
	}
}

func (s *Service) expire() {
	s.mu.Lock()
	changed := false
	for id, p := range s.peers {
		if time.Since(p.LastSeen) > peerTTL {
			delete(s.peers, id)
			changed = true
		}
	}
	cb := s.onChange
	s.mu.Unlock()
	if changed && cb != nil {
		cb()
	}
}

// Peers returns currently visible devices sorted by name.
func (s *Service) Peers() []Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Peer, 0, len(s.peers))
	for _, p := range s.peers {
		if time.Since(p.LastSeen) <= peerTTL {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ErrAmbiguous is returned when a query matches more than one device.
var ErrAmbiguous = errors.New("more than one device matches; use the ID prefix")

// Find waits until a device whose name (case-insensitive) or ID prefix
// matches query is visible.
func (s *Service) Find(ctx context.Context, query string) (Peer, error) {
	q := strings.ToLower(query)
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		var matches []Peer
		for _, p := range s.Peers() {
			if strings.ToLower(p.Name) == q || strings.HasPrefix(p.ID, q) {
				matches = append(matches, p)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return Peer{}, ErrAmbiguous
		}
		select {
		case <-ctx.Done():
			return Peer{}, ctx.Err()
		case <-t.C:
		}
	}
}

func (s *Service) selfAnn() Announce {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.self
}
