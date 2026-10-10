// Package lazylibrarian talks to LazyLibrarian, which finds and downloads books, for requests
// (docs/design/requests.md): it searches OpenLibrary through it, adds a book as wanted, asks for a
// search and reads where each book stands. It knows nothing about the database or the catalog.
package lazylibrarian

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const maxResponse = 32 << 20

// Client calls the API of a LazyLibrarian instance.
type Client struct {
	base string
	key  string
	http *http.Client
}

// New creates a client for the instance at base ("http://localhost:5299").
func New(base, apiKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: strings.TrimSuffix(base, "/"), key: apiKey, http: hc}
}

// Error is a refusal from LazyLibrarian, with its explanation.
type Error struct {
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrUnauthorized is returned when the API key is rejected.
var ErrUnauthorized = errors.New("API key rejected")

// failure is how LazyLibrarian refuses a call (bad key, API turned off).
type failure struct {
	Success *bool `json:"Success"`
	Error   struct {
		Code    int    `json:"Code"`
		Message string `json:"Message"`
	} `json:"Error"`
}

// call runs a command. out nil means the command answers with a line of text, "OK" on success.
func (c *Client) call(ctx context.Context, cmd string, params url.Values, out any) error {
	q := url.Values{"apikey": {c.key}, "cmd": {cmd}}
	for k, v := range params {
		q[k] = v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &Error{Message: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	var f failure
	if json.Unmarshal(data, &f) == nil && f.Success != nil && !*f.Success {
		if f.Error.Code == http.StatusUnauthorized {
			return ErrUnauthorized
		}
		return &Error{Message: f.Error.Message}
	}
	if out == nil {
		if text := strings.TrimSpace(string(data)); text != "OK" {
			return &Error{Message: text}
		}
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: unreadable response: %w", cmd, err)
	}
	return nil
}

// Version reads the version of the instance, and checks that it is LazyLibrarian.
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Current *string `json:"current_version"`
	}
	if err := c.call(ctx, "getVersion", nil, &v); err != nil {
		return "", err
	}
	if v.Current == nil {
		return "", errors.New("this address does not answer like LazyLibrarian")
	}
	return *v.Current, nil
}

// Book is a book found by a search or known to the instance.
type Book struct {
	// ID is the book's ID at the instance's metadata source (an OpenLibrary work ID by default).
	ID       string
	Title    string
	Author   string
	ISBN     string
	Overview string
	// Cover is the address of the cover; empty if none.
	Cover string
	// Year of publication, 0 if unknown.
	Year int
	// Status is the ebook's status at the instance: "Skipped", "Wanted", "Snatched", "Have",
	// "Open", "Ignored"...; empty for a book it does not know.
	Status string
}

// InLibrary reports a book the instance has downloaded and filed.
func (b Book) InLibrary() bool { return b.Status == "Have" || b.Status == "Open" }

// Wanted reports a book the instance looks for or downloads.
func (b Book) Wanted() bool { return b.Status == "Wanted" || b.Status == "Snatched" }

var year = regexp.MustCompile(`\b(1[0-9]{3}|20[0-9]{2})\b`)

func yearOf(values ...any) int {
	for _, v := range values {
		if m := year.FindString(fmt.Sprint(v)); m != "" {
			n, _ := strconv.Atoi(m)
			return n
		}
	}
	return 0
}

// cover keeps a web address only (the instance also gives paths of its own cache), and asks
// OpenLibrary for its large size rather than the thumbnail.
func cover(u string) string {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return ""
	}
	if strings.Contains(u, "covers.openlibrary.org/") {
		u = strings.Replace(strings.Replace(u, "-S.jpg", "-L.jpg", 1), "-M.jpg", "-L.jpg", 1)
		u = strings.Replace(u, "http://", "https://", 1)
	}
	return u
}

func text(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// found is a search result as the instance gives it.
type found struct {
	ID      string  `json:"bookid"`
	Title   any     `json:"bookname"`
	Author  any     `json:"authorname"`
	ISBN    any     `json:"bookisbn"`
	Desc    any     `json:"bookdesc"`
	Image   any     `json:"bookimg"`
	Date    any     `json:"bookdate"`
	Pub     any     `json:"bookpub"`
	Fuzz    float64 `json:"highest_fuzz"`
	Ratings float64 `json:"bookrate_count"`
}

// Search looks a book up, best match first, each book once.
func (c *Client) Search(ctx context.Context, term string) ([]Book, error) {
	var list []found
	if err := c.call(ctx, "findBook", url.Values{"name": {term}}, &list); err != nil {
		return nil, err
	}
	// The best match first; among equals, the most rated (the well-known edition of a title).
	slices.SortStableFunc(list, func(x, y found) int {
		return cmp.Or(cmp.Compare(y.Fuzz, x.Fuzz), cmp.Compare(y.Ratings, x.Ratings))
	})
	seen := map[string]bool{}
	out := make([]Book, 0, len(list))
	for _, b := range list {
		if b.ID == "" || seen[b.ID] {
			continue
		}
		seen[b.ID] = true
		out = append(out, Book{
			ID: b.ID, Title: text(b.Title), Author: text(b.Author), ISBN: text(b.ISBN), Overview: text(b.Desc),
			Cover: cover(text(b.Image)), Year: yearOf(b.Date, b.Pub),
		})
	}
	return out, nil
}

// Books lists the books the instance knows, with their status.
func (c *Client) Books(ctx context.Context) ([]Book, error) {
	var list []struct {
		ID     string `json:"BookID"`
		Title  any    `json:"BookName"`
		Author any    `json:"AuthorName"`
		ISBN   any    `json:"BookIsbn"`
		Image  any    `json:"BookImg"`
		Date   any    `json:"BookDate"`
		Status any    `json:"Status"`
	}
	if err := c.call(ctx, "getAllBooks", nil, &list); err != nil {
		return nil, err
	}
	out := make([]Book, len(list))
	for i, b := range list {
		out[i] = Book{
			ID: b.ID, Title: text(b.Title), Author: text(b.Author), ISBN: text(b.ISBN), Cover: cover(text(b.Image)),
			Year: yearOf(b.Date), Status: text(b.Status),
		}
	}
	return out, nil
}

// Want adds a book to the instance (waiting for its details to be read), marks the ebook as wanted
// and searches for it.
func (c *Client) Want(ctx context.Context, id string) error {
	if err := c.call(ctx, "addBook", url.Values{"id": {id}, "wait": {"1"}}, nil); err != nil {
		// The answer to an addition is a summary, "OK" only when there is nothing to say: the book is
		// checked by the next call.
		var refused *Error
		if !errors.As(err, &refused) {
			return err
		}
	}
	if err := c.call(ctx, "queueBook", url.Values{"id": {id}, "type": {"eBook"}}, nil); err != nil {
		return err
	}
	return c.call(ctx, "searchBook", url.Values{"id": {id}, "type": {"eBook"}}, nil)
}
