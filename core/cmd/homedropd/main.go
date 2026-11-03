// homedropd is the HomeDrop daemon and command-line tool.
//
//	homedropd serve                 receive files (and be discoverable)
//	homedropd list                  show devices on the network
//	homedropd send --to NAME FILES  send files/folders
//	homedropd send --to NAME --text "hello"
//	homedropd id                    show this device's name and fingerprint
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/janak/homedrop/internal/discovery"
	"github.com/janak/homedrop/internal/identity"
	"github.com/janak/homedrop/internal/transfer"
)

const defaultPort = 53530

func usage() {
	fmt.Fprint(os.Stderr, `HomeDrop — fast file transfer between your devices

usage:
  homedropd serve [--port N] [--dir DIR] [--auto-accept] [--clipboard]
  homedropd list
  homedropd send --to NAME|ID|IP:PORT [--text TEXT] [FILES/FOLDERS...]
  homedropd id
  homedropd daemon [--port N] [--control-port N]   (background service for the app)

global flags (put before the command's own flags):
  --name NAME   device name (default: hostname)
  --data DIR    where keys and trusted devices are stored
`)
	os.Exit(2)
}

type globals struct {
	name, data string
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = cmdServe(ctx, args)
	case "list":
		err = cmdList(ctx, args)
	case "send":
		err = cmdSend(ctx, args)
	case "id":
		err = cmdID(args)
	case "daemon":
		err = cmdDaemon(ctx, args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func addGlobals(fs *flag.FlagSet) *globals {
	g := &globals{}
	host, _ := os.Hostname()
	cfg, _ := os.UserConfigDir()
	fs.StringVar(&g.name, "name", host, "device name")
	fs.StringVar(&g.data, "data", filepath.Join(cfg, "homedrop"), "data directory")
	return g
}

func (g *globals) load() (*identity.Identity, *identity.TrustStore, error) {
	id, err := identity.Load(g.data, g.name)
	if err != nil {
		return nil, nil, err
	}
	trust, err := identity.LoadTrust(g.data)
	return id, trust, err
}

func cmdID(args []string) error {
	fs := flag.NewFlagSet("id", flag.ExitOnError)
	g := addGlobals(fs)
	_ = fs.Parse(args)
	id, _, err := g.load()
	if err != nil {
		return err
	}
	fmt.Printf("name:        %s\nfingerprint: %s\ndata dir:    %s\n", id.Name, id.Fingerprint, g.data)
	return nil
}

// ---------------------------------------------------------------- serve

func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	g := addGlobals(fs)
	home, _ := os.UserHomeDir()
	port := fs.Int("port", defaultPort, "TCP port to receive on")
	dir := fs.String("dir", filepath.Join(home, "Downloads", "HomeDrop"), "where received files go")
	autoAccept := fs.Bool("auto-accept", false, "accept automatically from already-trusted devices")
	clipboard := fs.Bool("clipboard", false, "copy received text to the clipboard")
	_ = fs.Parse(args)

	id, trust, err := g.load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		return err
	}

	lines := stdinLines()
	var promptMu sync.Mutex
	var progLine atomic.Bool

	hooks := transfer.Hooks{
		Decide: func(ctx context.Context, o *transfer.IncomingOffer) bool {
			promptMu.Lock()
			defer promptMu.Unlock()
			clearProgress(&progLine)
			fmt.Printf("\n📥 %s (%s) wants to send %s\n", o.SenderName, o.SenderOS, describeOffer(o))
			if !o.Trusted {
				fmt.Printf("   New device %s — verification code: %s (check it matches on the sender)\n",
					identity.Short(o.SenderID), o.PairCode)
			} else if *autoAccept {
				fmt.Println("   trusted device → accepted automatically")
				return true
			}
			fmt.Print("   Accept? [y/N] ")
			select {
			case l := <-lines:
				ok := strings.EqualFold(strings.TrimSpace(l), "y") || strings.EqualFold(strings.TrimSpace(l), "yes")
				if !ok {
					fmt.Println("   declined")
				}
				return ok
			case <-ctx.Done():
				fmt.Println("\n   (timed out / sender gave up)")
				return false
			}
		},
		Text: func(o *transfer.IncomingOffer, text string) {
			fmt.Printf("📋 Text from %s:\n%s\n", o.SenderName, text)
			if *clipboard {
				if err := copyToClipboard(text); err != nil {
					fmt.Println("   (could not copy to clipboard:", err, ")")
				} else {
					fmt.Println("   copied to clipboard")
				}
			}
		},
		Progress: func(_ string, o *transfer.IncomingOffer, done, total int64) {
			progLine.Store(true)
			fmt.Printf("\r   receiving from %s: %s", o.SenderName, progressText(done, total, 0))
		},
		SessionDone: func(_ string, o *transfer.IncomingOffer, dir string, err error) {
			clearProgress(&progLine)
			if err != nil {
				fmt.Printf("❌ transfer from %s failed: %v\n", o.SenderName, err)
				return
			}
			fmt.Printf("✅ received %d file(s), %s from %s → %s\n", len(o.Files), human(o.TotalSize), o.SenderName, dir)
		},
	}

	srv := transfer.NewServer(id, trust, *dir, hooks)
	disc := discovery.New(discovery.Announce{ID: id.Fingerprint, Name: id.Name, OS: runtime.GOOS, Port: *port}, true)
	if err := disc.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "warning: discovery unavailable:", err)
	}

	fmt.Printf("HomeDrop: %q (%s) receiving on port %d → %s\n", id.Name, identity.Short(id.Fingerprint), *port, *dir)
	fmt.Println("Press Ctrl+C to stop.")
	return srv.Serve(ctx, ln)
}

func clearProgress(flag *atomic.Bool) {
	if flag.Swap(false) {
		fmt.Print("\r\033[K")
	}
}

func stdinLines() <-chan string {
	ch := make(chan string)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			ch <- sc.Text()
		}
	}()
	return ch
}

func describeOffer(o *transfer.IncomingOffer) string {
	var parts []string
	if n := len(o.Files); n > 0 {
		if n == 1 {
			parts = append(parts, fmt.Sprintf("%q (%s)", o.Files[0].Path, human(o.TotalSize)))
		} else {
			roots := map[string]bool{}
			for _, f := range o.Files {
				roots[strings.SplitN(f.Path, "/", 2)[0]] = true
			}
			names := make([]string, 0, len(roots))
			for r := range roots {
				names = append(names, r)
			}
			if len(names) > 3 {
				names = append(names[:3], "…")
			}
			parts = append(parts, fmt.Sprintf("%d files, %s [%s]", n, human(o.TotalSize), strings.Join(names, ", ")))
		}
	}
	if o.Text != "" {
		parts = append(parts, fmt.Sprintf("text (%d chars)", len([]rune(o.Text))))
	}
	return strings.Join(parts, " + ")
}

func copyToClipboard(text string) error {
	for _, c := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}} {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	return errors.New("install wl-clipboard or xclip")
}

// ---------------------------------------------------------------- list

func cmdList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	g := addGlobals(fs)
	wait := fs.Duration("wait", 1500*time.Millisecond, "how long to listen")
	_ = fs.Parse(args)
	id, trust, err := g.load()
	if err != nil {
		return err
	}
	disc := discovery.New(discovery.Announce{ID: id.Fingerprint, Name: id.Name, OS: runtime.GOOS}, false)
	if err := disc.Start(ctx); err != nil {
		return err
	}
	select {
	case <-time.After(*wait):
	case <-ctx.Done():
	}
	peers := disc.Peers()
	if len(peers) == 0 {
		fmt.Println("No devices found. Is `homedropd serve` running on the other device, on the same network?")
		return nil
	}
	fmt.Printf("%-20s %-10s %-8s %-22s %s\n", "NAME", "ID", "OS", "ADDRESS", "TRUSTED")
	for _, p := range peers {
		t := ""
		if trust.Trusted(p.ID) {
			t = "yes"
		}
		fmt.Printf("%-20s %-10s %-8s %-22s %s\n", p.Name, identity.Short(p.ID), p.OS, p.Addr, t)
	}
	return nil
}

// ---------------------------------------------------------------- send

func cmdSend(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	g := addGlobals(fs)
	to := fs.String("to", "", "device name, ID prefix, or IP:PORT")
	text := fs.String("text", "", "text to send (shows up on the receiver, can go to its clipboard)")
	parallel := fs.Int("parallel", 4, "parallel file streams")
	_ = fs.Parse(args)
	paths := fs.Args()
	if *to == "" || (len(paths) == 0 && *text == "") {
		usage()
	}
	id, trust, err := g.load()
	if err != nil {
		return err
	}

	target, err := findTarget(ctx, id, trust, *to)
	if err != nil {
		return err
	}

	client := transfer.NewClient(id, trust)
	start := time.Now()
	var sendStart atomic.Int64
	var lastPrint atomic.Int64
	var total atomic.Int64
	opts := transfer.SendOptions{
		Parallel: *parallel,
		OnOffer: func(code string, trusted bool) {
			if !trusted {
				fmt.Printf("New device %q — verification code: %s (check it matches on the receiver)\n", target.Name, code)
			}
			fmt.Printf("Waiting for %s to accept…\n", target.Name)
		},
		Progress: func(done, tot int64) {
			sendStart.CompareAndSwap(0, time.Now().UnixNano())
			total.Store(tot)
			now := time.Now().UnixNano()
			last := lastPrint.Load()
			if now-last > int64(250*time.Millisecond) && lastPrint.CompareAndSwap(last, now) {
				el := time.Since(time.Unix(0, sendStart.Load()))
				fmt.Printf("\r\033[K%s", progressText(done, tot, el))
			}
		},
	}
	if err := client.Send(ctx, target, paths, *text, opts); err != nil {
		fmt.Println()
		return err
	}
	if total.Load() > 0 {
		el := time.Since(time.Unix(0, sendStart.Load()))
		fmt.Printf("\r\033[K✅ sent %s in %s (%s/s)\n", human(total.Load()), el.Round(10*time.Millisecond), human(int64(float64(total.Load())/el.Seconds())))
	} else {
		fmt.Printf("✅ delivered in %s\n", time.Since(start).Round(time.Millisecond))
	}
	return nil
}

func findTarget(ctx context.Context, id *identity.Identity, trust *identity.TrustStore, to string) (transfer.Target, error) {
	if _, _, err := net.SplitHostPort(to); err == nil {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return transfer.NewClient(id, trust).Resolve(rctx, to)
	}
	disc := discovery.New(discovery.Announce{ID: id.Fingerprint, Name: id.Name, OS: runtime.GOOS}, false)
	if err := disc.Start(ctx); err != nil {
		return transfer.Target{}, err
	}
	fctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	p, err := disc.Find(fctx, to)
	if errors.Is(err, context.DeadlineExceeded) {
		return transfer.Target{}, fmt.Errorf("device %q not found (try `homedropd list`, or --to IP:PORT)", to)
	}
	if err != nil {
		return transfer.Target{}, err
	}
	return transfer.Target{ID: p.ID, Name: p.Name, Addr: p.Addr}, nil
}

// ---------------------------------------------------------------- formatting

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func progressText(done, total int64, elapsed time.Duration) string {
	pct := 100.0
	if total > 0 {
		pct = float64(done) * 100 / float64(total)
	}
	s := fmt.Sprintf("%5.1f%%  %s / %s", pct, human(done), human(total))
	if elapsed > 200*time.Millisecond {
		s += fmt.Sprintf("  %s/s", human(int64(float64(done)/elapsed.Seconds())))
	}
	return s
}
