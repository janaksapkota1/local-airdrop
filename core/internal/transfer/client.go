package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/janak/homedrop/internal/identity"
)

// Target is a device to send to.
type Target struct {
	ID   string // fingerprint; the TLS connection is pinned to it
	Name string
	Addr string // host:port
}

type SendOptions struct {
	Parallel int // concurrent file uploads (default 4)
	// OnOffer is called before waiting for the receiver to accept. If the
	// device is new, show code so the user can compare it on both screens.
	OnOffer    func(code string, trusted bool)
	OnAccepted func()
	Progress   func(done, total int64) // called from worker goroutines
}

var (
	ErrDeclined  = errors.New("receiver declined")
	ErrCancelled = errors.New("receiver cancelled the transfer")
	ErrNotPaired = errors.New("devices are not paired yet")
)

// SendMessage delivers one chat message. It is safe to retry with the same ID.
func (c *Client) SendMessage(ctx context.Context, t Target, m Message) error {
	hc := c.httpClient(t.ID, 1)
	defer hc.CloseIdleConnections()
	body, _ := json.Marshal(m)
	err := doJSON(ctx, hc, "POST", "https://"+t.Addr+"/v1/message", body, nil)
	var he *httpError
	if errors.As(err, &he) && he.code == 403 {
		return ErrNotPaired
	}
	return err
}

type Client struct {
	id    *identity.Identity
	trust *identity.TrustStore

	failAfter atomic.Int64 // test hook: abort the first upload after this many bytes
	wireBytes atomic.Int64 // file bytes put on the wire, including retries
	chunkSize int64        // 0 = DefaultChunkSize (tests use smaller chunks)
}

func NewClient(id *identity.Identity, trust *identity.TrustStore) *Client {
	return &Client{id: id, trust: trust}
}

func (c *Client) httpClient(expectID string, parallel int) *http.Client {
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig:     c.id.ClientTLS(expectID),
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConnsPerHost: parallel,
		DisableCompression:  true,
		WriteBufferSize:     256 << 10,
		ReadBufferSize:      64 << 10,
	}}
}

// Resolve looks up a device by address (for when discovery is blocked).
// The returned ID is whatever certificate the device presented, so the pair
// code must be checked on first contact.
func (c *Client) Resolve(ctx context.Context, addr string) (Target, error) {
	hc := c.httpClient("", 1)
	defer hc.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+addr+"/v1/info", nil)
	resp, err := hc.Do(req)
	if err != nil {
		return Target{}, err
	}
	defer resp.Body.Close()
	var info Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return Target{}, err
	}
	fp := identity.Fingerprint(resp.TLS.PeerCertificates[0].Raw)
	if info.ID != fp {
		return Target{}, errors.New("device info does not match its certificate")
	}
	return Target{ID: fp, Name: info.Name, Addr: addr}, nil
}

// Send offers paths and/or text to t and uploads the files once accepted.
func (c *Client) Send(ctx context.Context, t Target, paths []string, text string, opt SendOptions) error {
	if opt.Parallel <= 0 {
		opt.Parallel = 4
	}
	var files []localFile
	if len(paths) > 0 {
		var err error
		if files, err = BuildManifest(paths); err != nil {
			return err
		}
	}
	var total int64
	items := make([]Item, len(files))
	for i, f := range files {
		items[i] = f.Item
		total += f.Size
	}

	hc := c.httpClient(t.ID, opt.Parallel)
	defer hc.CloseIdleConnections()
	base := "https://" + t.Addr

	trusted := c.trust.Trusted(t.ID)
	if opt.OnOffer != nil {
		opt.OnOffer(identity.PairCode(t.ID, c.id.Fingerprint), trusted)
	}
	host, _ := os.Hostname()
	if c.id.Name != "" {
		host = c.id.Name
	}
	chunkSize := c.chunkSize
	if chunkSize == 0 {
		chunkSize = DefaultChunkSize
	}
	body, _ := json.Marshal(Offer{SenderName: host, SenderOS: runtime.GOOS, Files: items, Text: text, ChunkSize: chunkSize})
	var or OfferResponse
	if err := doJSON(ctx, hc, "POST", base+"/v1/offer", body, &or); err != nil {
		return fmt.Errorf("offer: %w", err)
	}
	if !or.Accepted {
		if or.Reason != "" && or.Reason != "declined" {
			return fmt.Errorf("%w: %s", ErrDeclined, or.Reason)
		}
		return ErrDeclined
	}
	if !trusted {
		_ = c.trust.Add(t.ID, t.Name)
	}
	if opt.OnAccepted != nil {
		opt.OnAccepted()
	}
	if len(files) == 0 {
		return nil
	}

	up := &uploader{c: c, hc: hc, base: base, session: or.Session, total: total, progress: opt.Progress}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan chunkJob)
	errs := make(chan error, opt.Parallel)
	var wg sync.WaitGroup
	for range opt.Parallel {
		wg.Go(func() {
			for j := range jobs {
				if err := up.sendWithRetry(ctx, j); err != nil {
					if !errors.Is(err, ErrCancelled) {
						err = fmt.Errorf("%s: %w", j.f.Path, err)
					}
					errs <- err
					cancel()
					return
				}
			}
		})
	}
	// Big files first, each split into chunks, so the parallel streams stay
	// busy until the very end — even when there is only one file.
	order := make([]localFile, len(files))
	copy(order, files)
	sortBySizeDesc(order)
feed:
	for _, f := range order {
		n := max(1, int((f.Size+chunkSize-1)/chunkSize))
		for i := range n {
			off := int64(i) * chunkSize
			select {
			case jobs <- chunkJob{f: f, index: i, off: off, length: min(chunkSize, f.Size-off)}:
			case <-ctx.Done():
				break feed
			}
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)

	if err := <-errs; err != nil {
		c.cancelRemote(hc, base, or.Session)
		return err
	}
	if err := ctx.Err(); err != nil {
		c.cancelRemote(hc, base, or.Session)
		return err
	}
	return nil
}

func (c *Client) cancelRemote(hc *http.Client, base, session string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = doJSON(ctx, hc, "POST", base+"/v1/cancel?session="+url.QueryEscape(session), nil, nil)
}

func sortBySizeDesc(fs []localFile) {
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Size > fs[j].Size })
}

type uploader struct {
	c        *Client
	hc       *http.Client
	base     string
	session  string
	total    int64
	done     atomic.Int64
	progress func(done, total int64)
}

type chunkJob struct {
	f           localFile
	index       int
	off, length int64
}

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.code, e.msg) }

func (u *uploader) sendWithRetry(ctx context.Context, j chunkJob) error {
	const attempts = 6
	for i := 0; ; i++ {
		err := u.upload(ctx, j)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || i == attempts-1 {
			return err
		}
		var he *httpError
		if errors.As(err, &he) && he.code == 404 {
			return ErrCancelled
		}
		if errors.As(err, &he) && he.code == 400 && he.msg == "more data than announced" {
			return err // file changed while sending: retrying won't help
		}
		select {
		case <-time.After(time.Duration(i+1) * 400 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// trailerBody streams one chunk while computing its CRC32C, then sets the
// checksum trailer once the whole chunk has been read.
type trailerBody struct {
	r        io.Reader
	crc      uint32
	req      *http.Request
	u        *uploader
	counted  int64
	failLeft int64
}

func (b *trailerBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.crc = crc32.Update(b.crc, castagnoli, p[:n])
		b.counted += int64(n)
		b.u.c.wireBytes.Add(int64(n))
		d := b.u.done.Add(int64(n))
		if b.u.progress != nil {
			b.u.progress(d, b.u.total)
		}
		if b.failLeft > 0 {
			if b.failLeft -= int64(n); b.failLeft <= 0 {
				return n, errors.New("test: simulated network failure")
			}
		}
	}
	if err == io.EOF {
		b.req.Trailer.Set(crcTrailer, fmt.Sprintf("%08x", b.crc))
	}
	return n, err
}

func (u *uploader) upload(ctx context.Context, j chunkJob) error {
	fh, err := os.Open(j.f.Src)
	if err != nil {
		return err
	}
	defer fh.Close()

	q := url.Values{"session": {u.session}, "file": {strconv.Itoa(j.f.ID)}, "chunk": {strconv.Itoa(j.index)}}
	body := &trailerBody{r: io.NewSectionReader(fh, j.off, j.length), u: u}
	if fa := u.c.failAfter.Swap(0); fa > 0 {
		body.failLeft = fa
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", u.base+"/v1/upload?"+q.Encode(), body)
	if err != nil {
		return err
	}
	body.req = req
	req.ContentLength = -1 // chunked, so the checksum can go in a trailer
	req.Trailer = http.Header{crcTrailer: nil}
	req.GetBody = nil

	resp, err := u.hc.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			err = readHTTPError(resp)
		} else {
			_, _ = io.Copy(io.Discard, resp.Body)
		}
	}
	if err != nil {
		u.done.Add(-body.counted) // this chunk will be sent again
	}
	return err
}

func readHTTPError(resp *http.Response) error {
	var er errorResponse
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(data, &er) != nil {
		er.Error = string(data)
	}
	return &httpError{code: resp.StatusCode, msg: er.Error}
}

func doJSON(ctx context.Context, hc *http.Client, method, u string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return readHTTPError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
