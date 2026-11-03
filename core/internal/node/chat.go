package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/janak/homedrop/internal/transfer"
)

// One-to-one chat with each device. Text goes over POST /v1/message between
// paired devices; file transfers to/from a device also appear in its chat.
// History is kept in <data>/chats.json.

const maxChatHistory = 2000

// Message statuses.
const (
	MsgSending  = "sending"
	MsgSent     = "sent"
	MsgQueued   = "queued" // device offline; sent automatically when it appears
	MsgFailed   = "failed"
	MsgReceived = "received"
)

// FilesSummary describes a file transfer shown inside a chat.
type FilesSummary struct {
	FileCount int      `json:"fileCount"`
	Roots     []string `json:"roots"`
	Total     int64    `json:"total"`
	State     string   `json:"state"`
	Dir       string   `json:"dir,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type ChatMessage struct {
	ID         string        `json:"id"`
	PeerID     string        `json:"peerId"`
	Out        bool          `json:"out"`
	Kind       string        `json:"kind"` // "text" | "files"
	Text       string        `json:"text,omitempty"`
	TransferID string        `json:"transferId,omitempty"`
	Files      *FilesSummary `json:"files,omitempty"`
	Time       time.Time     `json:"time"`
	Status     string        `json:"status"`
	Error      string        `json:"error,omitempty"`
}

type Conversation struct {
	PeerID   string        `json:"peerId"`
	PeerName string        `json:"peerName"`
	Unread   int           `json:"unread"`
	Messages []ChatMessage `json:"messages"`
}

type ChatSummary struct {
	PeerID   string       `json:"peerId"`
	PeerName string       `json:"peerName"`
	Unread   int          `json:"unread"`
	Online   bool         `json:"online"`
	Trusted  bool         `json:"trusted"`
	Last     *ChatMessage `json:"last,omitempty"`
}

type chatEvent struct {
	PeerID   string      `json:"peerId"`
	PeerName string      `json:"peerName"`
	Unread   int         `json:"unread"`
	Message  ChatMessage `json:"message"`
}

func chatsPath(dir string) string { return filepath.Join(dir, "chats.json") }

func loadChats(dir string) map[string]*Conversation {
	chats := map[string]*Conversation{}
	if data, err := os.ReadFile(chatsPath(dir)); err == nil {
		_ = json.Unmarshal(data, &chats)
	}
	// Anything mid-flight when we stopped will never finish now.
	for _, c := range chats {
		for i := range c.Messages {
			m := &c.Messages[i]
			if m.Status == MsgSending && m.Kind == "text" {
				m.Status = MsgQueued
			}
			if m.Files != nil && (m.Files.State == StateRunning || m.Files.State == StateWaiting) {
				m.Files.State, m.Status = StateFailed, MsgFailed
			}
		}
	}
	return chats
}

func (n *Node) saveChatsLocked() {
	data, err := json.Marshal(n.chats)
	if err != nil {
		return
	}
	tmp := chatsPath(n.cfg.DataDir) + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, chatsPath(n.cfg.DataDir))
	}
}

func (n *Node) convLocked(peerID, peerName string) *Conversation {
	c := n.chats[peerID]
	if c == nil {
		c = &Conversation{PeerID: peerID}
		n.chats[peerID] = c
	}
	if peerName != "" {
		c.PeerName = peerName
	}
	return c
}

func (n *Node) emitChatLocked(c *Conversation, m ChatMessage) {
	n.emitLocked(Event{"chat", chatEvent{PeerID: c.PeerID, PeerName: c.PeerName, Unread: c.Unread, Message: m}})
}

// addMessageLocked appends a message (or ignores a duplicate ID) and saves.
func (n *Node) addMessageLocked(peerName string, m ChatMessage) {
	c := n.convLocked(m.PeerID, peerName)
	for _, old := range c.Messages {
		if old.ID == m.ID {
			return // retry of a message we already have
		}
	}
	c.Messages = append(c.Messages, m)
	if len(c.Messages) > maxChatHistory {
		c.Messages = c.Messages[len(c.Messages)-maxChatHistory:]
	}
	if !m.Out {
		c.Unread++
	}
	n.emitChatLocked(c, m)
	n.saveChatsLocked()
}

// updateMessagesLocked applies fn to every message of peerID matching pred.
func (n *Node) updateMessagesLocked(peerID string, pred func(*ChatMessage) bool, fn func(*ChatMessage)) {
	c := n.chats[peerID]
	if c == nil {
		return
	}
	changed := false
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if m := &c.Messages[i]; pred(m) {
			fn(m)
			n.emitChatLocked(c, *m)
			changed = true
		}
	}
	if changed {
		n.saveChatsLocked()
	}
}

// transferToChatLocked records a new transfer in the peer's chat.
func (n *Node) transferToChatLocked(t *TransferView) {
	m := ChatMessage{
		PeerID: t.PeerID, Out: t.Direction == "send", Time: t.Started, TransferID: t.ID,
	}
	if t.FileCount > 0 {
		m.ID, m.Kind = "f-"+t.ID, "files"
		m.Files = &FilesSummary{FileCount: t.FileCount, Roots: t.Roots, Total: t.Total, State: t.State}
		m.Status = MsgSending
		if !m.Out {
			m.Status = MsgReceived
		}
		n.addMessageLocked(t.PeerName, m)
	}
	if t.Text != "" && t.Direction == "send" {
		n.addMessageLocked(t.PeerName, ChatMessage{
			ID: "t-" + t.ID, PeerID: t.PeerID, Out: true, Kind: "text", Text: t.Text,
			TransferID: t.ID, Time: t.Started, Status: MsgSending,
		})
	}
}

// transferStateToChatLocked updates the chat once a transfer ends.
func (n *Node) transferStateToChatLocked(t *TransferView) {
	if t.State == StateRunning || t.State == StateWaiting {
		return
	}
	n.updateMessagesLocked(t.PeerID,
		func(m *ChatMessage) bool { return m.TransferID == t.ID },
		func(m *ChatMessage) {
			if m.Files != nil {
				m.Files.State, m.Files.Dir, m.Files.Error = t.State, t.Dir, t.Error
			}
			if m.Out {
				if t.State == StateDone {
					m.Status = MsgSent
				} else {
					m.Status, m.Error = MsgFailed, t.Error
				}
			}
		})
}

// SendChat sends a text message to a device.
//   - paired + online:  delivered directly
//   - paired + offline: queued; sent when the device shows up
//   - not paired:       goes as an offer, so the other side sees the
//     Accept prompt with the pairing code (once)
func (n *Node) SendChat(peerID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty message")
	}
	if len(text) > 64<<10 {
		return errors.New("message too long")
	}
	peer, online := n.peer(peerID)
	trusted := n.trust.Trusted(peerID)
	if !trusted {
		if !online {
			return errors.New("this device is offline, and you have not paired with it yet")
		}
		_, err := n.Send(SendRequest{To: peerID, Text: text})
		return err
	}

	n.mu.Lock()
	m := ChatMessage{ID: newID(), PeerID: peerID, Out: true, Kind: "text", Text: text, Time: time.Now(), Status: MsgQueued}
	if online {
		m.Status = MsgSending
	}
	n.addMessageLocked(peer.Name, m)
	n.mu.Unlock()

	if online {
		go n.deliver(peer, m)
	}
	return nil
}

func (n *Node) peer(id string) (PeerView, bool) {
	for _, p := range n.Peers() {
		if p.ID == id {
			return p, true
		}
	}
	return PeerView{ID: id}, false
}

func (n *Node) deliver(p PeerView, m ChatMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	name := n.Self().Name
	err := n.client.SendMessage(ctx, transfer.Target{ID: p.ID, Name: p.Name, Addr: p.Addr},
		transfer.Message{ID: m.ID, SenderName: name, Text: m.Text, SentAt: m.Time})

	n.mu.Lock()
	defer n.mu.Unlock()
	n.updateMessagesLocked(p.ID, func(x *ChatMessage) bool { return x.ID == m.ID }, func(x *ChatMessage) {
		switch {
		case err == nil:
			x.Status, x.Error = MsgSent, ""
		case errors.Is(err, transfer.ErrNotPaired):
			x.Status, x.Error = MsgFailed, "the other device removed the pairing; send it a file to pair again"
		default:
			x.Status, x.Error = MsgQueued, "" // try again when we next see it
		}
	})
}

// flushQueued sends queued messages to devices that just came online.
func (n *Node) flushQueued() {
	online := map[string]PeerView{}
	for _, p := range n.Peers() {
		if p.Trusted {
			online[p.ID] = p
		}
	}
	type job struct {
		p PeerView
		m ChatMessage
	}
	var jobs []job
	n.mu.Lock()
	for id, c := range n.chats {
		p, ok := online[id]
		if !ok {
			continue
		}
		for i := range c.Messages {
			if m := &c.Messages[i]; m.Out && m.Kind == "text" && m.Status == MsgQueued && m.TransferID == "" {
				m.Status = MsgSending
				n.emitChatLocked(c, *m)
				jobs = append(jobs, job{p, *m})
			}
		}
	}
	n.mu.Unlock()
	for _, j := range jobs {
		go n.deliver(j.p, j.m)
	}
}

// receiveMessage is the transfer-server hook for incoming chat messages.
func (n *Node) receiveMessage(fromID string, m transfer.Message) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t := m.SentAt
	if t.IsZero() || t.After(time.Now().Add(time.Minute)) {
		t = time.Now() // don't trust a skewed clock to reorder the chat
	}
	n.addMessageLocked(m.SenderName, ChatMessage{
		ID: m.ID, PeerID: fromID, Kind: "text", Text: m.Text, Time: t, Status: MsgReceived,
	})
}

// receiveOfferText puts text that came with an accepted offer into the chat.
func (n *Node) receiveOfferTextLocked(o *transfer.IncomingOffer, text string) {
	n.addMessageLocked(o.SenderName, ChatMessage{
		ID: newID(), PeerID: o.SenderID, Kind: "text", Text: text, Time: time.Now(), Status: MsgReceived,
	})
}

// Messages returns a device's chat history, oldest first.
func (n *Node) Messages(peerID string) []ChatMessage {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := n.chats[peerID]
	if c == nil {
		return []ChatMessage{}
	}
	return append([]ChatMessage(nil), c.Messages...)
}

// MarkRead clears a conversation's unread count.
func (n *Node) MarkRead(peerID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if c := n.chats[peerID]; c != nil && c.Unread > 0 {
		c.Unread = 0
		n.emitLocked(Event{"chat_read", map[string]string{"peerId": peerID}})
		n.saveChatsLocked()
	}
}

// DeleteChat removes a conversation's history.
func (n *Node) DeleteChat(peerID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.chats, peerID)
	n.emitLocked(Event{"chat_deleted", map[string]string{"peerId": peerID}})
	n.saveChatsLocked()
}

// Chats lists conversations, most recent first.
func (n *Node) Chats() []ChatSummary {
	online := map[string]bool{}
	for _, p := range n.Peers() {
		online[p.ID] = true
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.chatsLocked(online)
}

func (n *Node) chatsLocked(online map[string]bool) []ChatSummary {
	out := make([]ChatSummary, 0, len(n.chats))
	for _, c := range n.chats {
		s := ChatSummary{PeerID: c.PeerID, PeerName: c.PeerName, Unread: c.Unread,
			Online: online[c.PeerID], Trusted: n.trust.Trusted(c.PeerID)}
		if len(c.Messages) > 0 {
			last := c.Messages[len(c.Messages)-1]
			s.Last = &last
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Last == nil || out[j].Last == nil {
			return out[j].Last == nil
		}
		return out[i].Last.Time.After(out[j].Last.Time)
	})
	return out
}
