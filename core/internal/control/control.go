// Package control exposes a node to the local UI over HTTP on 127.0.0.1.
//
//	GET  /api/state                     full snapshot
//	GET  /api/events                    Server-Sent Events stream of node.Event
//	POST /api/send                      {to|addr, paths, text} → {id}
//	POST /api/offers/{id}               {accept: bool}
//	POST /api/transfers/{id}/cancel
//	POST /api/transfers/clear
//	PUT  /api/settings                  node.Settings
//	POST /api/refresh                   re-scan for devices
//	GET  /api/chats                     conversations, most recent first
//	GET  /api/chats/{peer}              messages with one device
//	POST /api/chats/{peer}/messages     {text}
//	POST /api/chats/{peer}/read         clear unread count
//	DELETE /api/chats/{peer}            delete history
//
// Every request needs "Authorization: Bearer <token>".
package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/janak/homedrop/internal/node"
)

// Endpoint tells a UI how to reach the control API.
type Endpoint struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

func NewToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Listen binds the control API to 127.0.0.1:port (0 = any free port).
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

// Start brings up a node plus its control API and returns how to reach it.
// Both stop when ctx is cancelled.
func Start(ctx context.Context, cfg node.Config, ctlPort int) (*node.Node, Endpoint, error) {
	n, err := node.Start(ctx, cfg)
	if err != nil {
		return nil, Endpoint{}, err
	}
	ln, err := Listen(ctlPort)
	if err != nil {
		return nil, Endpoint{}, err
	}
	ep := Endpoint{Port: ln.Addr().(*net.TCPAddr).Port, Token: NewToken()}
	go func() { _ = Serve(ctx, n, ln, ep.Token) }()
	return n, ep, nil
}

// Serve runs the control API until ctx is cancelled.
func Serve(ctx context.Context, n *node.Node, ln net.Listener, token string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, n.State())
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) { events(w, r, n) })
	mux.HandleFunc("POST /api/send", func(w http.ResponseWriter, r *http.Request) {
		var req node.SendRequest
		if !readJSON(w, r, &req) {
			return
		}
		id, err := n.Send(req)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]string{"id": id})
	})
	mux.HandleFunc("POST /api/offers/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Accept bool `json:"accept"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		if err := n.Decide(r.PathValue("id"), req.Accept); err != nil {
			writeErr(w, 404, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/transfers/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := n.Cancel(r.PathValue("id")); err != nil {
			writeErr(w, 404, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/transfers/clear", func(w http.ResponseWriter, r *http.Request) {
		n.ClearFinished()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var s node.Settings
		if !readJSON(w, r, &s) {
			return
		}
		s, err := n.UpdateSettings(s)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, s)
	})
	mux.HandleFunc("GET /api/chats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, n.Chats())
	})
	mux.HandleFunc("GET /api/chats/{peer}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, n.Messages(r.PathValue("peer")))
	})
	mux.HandleFunc("POST /api/chats/{peer}/messages", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Text string `json:"text"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		if err := n.SendChat(r.PathValue("peer"), req.Text); err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/chats/{peer}/read", func(w http.ResponseWriter, r *http.Request) {
		n.MarkRead(r.PathValue("peer"))
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("DELETE /api/chats/{peer}", func(w http.ResponseWriter, r *http.Request) {
		n.DeleteChat(r.PathValue("peer"))
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/refresh", func(w http.ResponseWriter, r *http.Request) {
		n.Refresh()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	srv := &http.Server{Handler: auth(token, mux), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func auth(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			writeErr(w, 401, errors.New("unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func events(w http.ResponseWriter, r *http.Request, n *node.Node) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, errors.New("streaming unsupported"))
		return
	}
	ch, unsub := n.Subscribe()
	defer unsub()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	// The client should fetch /api/state after this "hello" to sync up.
	fmt.Fprint(w, "data: {\"type\":\"hello\"}\n\n")
	fl.Flush()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
		case e := <-ch:
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fl.Flush()
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(v); err != nil {
		writeErr(w, 400, err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
