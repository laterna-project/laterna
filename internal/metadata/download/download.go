// Package download fetches the images an NFO gives a URL for (posters, thumbs, cast photos). It is
// polite: requests to a site are spaced out, the User-Agent says who we are, and the wait asked by
// a 429 is honored. It is careful: the size is capped, the type is checked, and the file is written
// to a temporary name and renamed once complete.
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable is returned for an image that retrying will not get: bad URL, refused or not found
// (4xx), a response that is not an image or is too big.
var ErrUnavailable = errors.New("image unavailable")

const (
	// MaxSize caps a downloaded image.
	MaxSize      = 20 << 20
	maxRetries   = 2
	maxRetryWait = time.Minute
)

// Client downloads images, at most one request every every per site.
type Client struct {
	http  *http.Client
	agent string
	every time.Duration

	mu   sync.Mutex
	next map[string]time.Time
}

// New creates a client. A nil hc means the default client (30 s timeout).
func New(userAgent string, every time.Duration, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{http: hc, agent: userAgent, every: every, next: map[string]time.Time{}}
}

// Image saves the image at URL u to dst.
func (c *Client) Image(ctx context.Context, u, dst string) error {
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%w: invalid address %q", ErrUnavailable, u)
	}
	for attempt := 0; ; attempt++ {
		if err := c.wait(ctx, parsed.Host); err != nil {
			return err
		}
		retry, err := c.fetch(ctx, u, dst)
		if retry > 0 && attempt < maxRetries {
			c.pause(parsed.Host, retry)
			continue
		}
		return err
	}
}

// fetch makes one request. retry > 0 asks for another try after that delay (429).
func (c *Client) fetch(ctx context.Context, u, dst string) (retry time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", c.agent)
	req.Header.Set("Accept", "image/*")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return retryAfter(resp.Header.Get("Retry-After")), fmt.Errorf("image %s: HTTP 429", u)
	case resp.StatusCode/100 == 4:
		return 0, fmt.Errorf("%w: %s: HTTP %d", ErrUnavailable, u, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return 0, fmt.Errorf("image %s: HTTP %d", u, resp.StatusCode)
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); !strings.HasPrefix(mt, "image/") {
		return 0, fmt.Errorf("%w: %s: type %q", ErrUnavailable, u, mt)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".telechargement-*")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no effect once renamed
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, MaxSize+1))
	if err = errors.Join(err, tmp.Close()); err != nil {
		return 0, err
	}
	if n > MaxSize {
		return 0, fmt.Errorf("%w: %s: more than %d MB", ErrUnavailable, u, MaxSize>>20)
	}
	return 0, os.Rename(tmp.Name(), dst)
}

// wait waits for its turn: requests to the same site are at least every apart.
func (c *Client) wait(ctx context.Context, host string) error {
	c.mu.Lock()
	now := time.Now()
	at := c.next[host]
	if at.Before(now) {
		at = now
	}
	c.next[host] = at.Add(c.every)
	c.mu.Unlock()
	if d := at.Sub(now); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return nil
}

// pause pushes back requests to a site by d (after a 429).
func (c *Client) pause(host string, d time.Duration) {
	c.mu.Lock()
	if until := time.Now().Add(d); until.After(c.next[host]) {
		c.next[host] = until
	}
	c.mu.Unlock()
}

// retryAfter reads the requested wait (in seconds), capped; 10 s if missing.
func retryAfter(v string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second+time.Second, maxRetryWait)
	}
	return 10 * time.Second
}
