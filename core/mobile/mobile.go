// Package mobile is the gomobile entry point used by the Android app.
// Build with: gomobile bind -target=android -androidapi 26 -o homedrop.aar ./mobile
package mobile

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/janak/homedrop/internal/control"
	"github.com/janak/homedrop/internal/node"
)

var (
	mu       sync.Mutex
	cancel   context.CancelFunc
	endpoint string
)

// Start brings the device online and returns the control endpoint as JSON
// {"port":N,"token":"..."}. Calling it again while running returns the same.
func Start(dataDir, downloadDir, name string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if cancel != nil {
		return endpoint, nil
	}
	ctx, c := context.WithCancel(context.Background())
	_, ep, err := control.Start(ctx, node.Config{
		DataDir: dataDir,
		OS:      "android",
		Defaults: node.Settings{
			Name:        name,
			DownloadDir: downloadDir,
		},
	}, 0)
	if err != nil {
		c()
		return "", err
	}
	out, _ := json.Marshal(ep)
	cancel, endpoint = c, string(out)
	return endpoint, nil
}

// Stop takes the device offline.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if cancel != nil {
		cancel()
		cancel, endpoint = nil, ""
	}
}
