// Package transfer implements the HomeDrop HTTPS transfer protocol:
//
//	GET  /v1/info                       device info
//	POST /v1/offer                      manifest → accept/decline (blocks on user)
//	PUT  /v1/upload?session=&file=&chunk=    one chunk's raw bytes, CRC32C in trailer
//	POST /v1/cancel?session=
//	POST /v1/message                    chat message (paired devices only)
//
// Files are split into fixed-size chunks (chosen by the sender) that can be
// uploaded in parallel and in any order, so even one big file uses several
// TCP streams. A failed chunk is simply sent again.
//
// Both sides present certificates (mutual TLS); a device's ID is the SHA-256
// fingerprint of its certificate.
package transfer

import (
	"encoding/json"
	"hash/crc32"
	"net/http"
	"time"
)

const (
	ProtocolVersion = 2
	partSuffix      = ".homedrop-part"
	crcTrailer      = "X-Crc32c"
	maxManifest     = 32 << 20
	maxFiles        = 200_000

	DefaultChunkSize = 16 << 20
	minChunkSize     = 64 << 10
	maxChunkSize     = 1 << 30
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

type Info struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OS      string `json:"os"`
	Version int    `json:"version"`
}

// Item is one file in an offer. Path uses "/" separators and is relative.
type Item struct {
	ID    int    `json:"id"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime,omitempty"` // unix seconds
}

type Offer struct {
	SenderName string `json:"senderName"`
	SenderOS   string `json:"senderOs"`
	Files      []Item `json:"files,omitempty"`
	Text       string `json:"text,omitempty"`
	ChunkSize  int64  `json:"chunkSize,omitempty"`
}

// Message is a chat message. Only devices that were paired before (accepted
// a transfer) may send these; anyone else must go through an offer first.
type Message struct {
	ID         string    `json:"id"` // chosen by the sender; makes retries idempotent
	SenderName string    `json:"senderName"`
	Text       string    `json:"text"`
	SentAt     time.Time `json:"sentAt"`
}

const maxMessageText = 64 << 10

type OfferResponse struct {
	Accepted bool   `json:"accepted"`
	Session  string `json:"session,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errorResponse{Error: msg})
}
