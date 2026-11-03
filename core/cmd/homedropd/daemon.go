package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/janak/homedrop/internal/control"
	"github.com/janak/homedrop/internal/node"
)

// cmdDaemon runs the device in the background for the desktop app. It writes
// its control endpoint to <data>/control.json (mode 0600) and prints it as a
// JSON line on stdout.
func cmdDaemon(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	host, _ := os.Hostname()
	cfg, _ := os.UserConfigDir()
	data := fs.String("data", filepath.Join(cfg, "homedrop"), "data directory")
	port := fs.Int("port", defaultPort, "transfer port")
	ctlPort := fs.Int("control-port", 0, "control API port on 127.0.0.1 (0 = random)")
	_ = fs.Parse(args)

	ctlFile := filepath.Join(*data, "control.json")
	if ep, ok := runningDaemon(ctlFile); ok {
		out, _ := json.Marshal(ep)
		fmt.Println(string(out))
		return errors.New("a daemon is already running")
	}

	_, ep, err := control.Start(ctx, node.Config{DataDir: *data, Port: *port, Defaults: node.Settings{Name: host}}, *ctlPort)
	if err != nil {
		return err
	}
	out, _ := json.Marshal(ep)
	if err := os.WriteFile(ctlFile, out, 0o600); err != nil {
		return err
	}
	defer os.Remove(ctlFile)
	fmt.Println(string(out))

	<-ctx.Done()
	return nil
}

// runningDaemon reports whether control.json points at a live daemon.
func runningDaemon(path string) (control.Endpoint, bool) {
	var ep control.Endpoint
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &ep) != nil || ep.Port == 0 {
		return ep, false
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/api/state", ep.Port), nil)
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	resp, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		return ep, false
	}
	resp.Body.Close()
	return ep, resp.StatusCode == 200
}
