// Package arr talks to Sonarr and Radarr (API v3), which write the NFO files and images Laterna
// reads. It checks and sets up their Kodi metadata, installs the webhook that tells Laterna about
// an import, asks for a refresh and lists the tracked folders. It knows nothing about the database
// or the catalog.
package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Kind is Sonarr or Radarr.
type Kind string

// Known integrations.
const (
	Sonarr Kind = "sonarr"
	Radarr Kind = "radarr"
)

// Kinds lists the integrations in display order.
var Kinds = []Kind{Sonarr, Radarr}

// Name is the display name.
func (k Kind) Name() string {
	if k == Radarr {
		return "Radarr"
	}
	return "Sonarr"
}

// WebhookName is the name of Laterna's webhook in Sonarr or Radarr.
const WebhookName = "Laterna"

const maxResponse = 32 << 20

// Client calls the API of a Sonarr or Radarr instance.
type Client struct {
	kind Kind
	base string
	key  string
	http *http.Client
}

// New creates a client for the instance at base ("http://localhost:8989").
func New(kind Kind, base, apiKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{kind: kind, base: strings.TrimSuffix(base, "/"), key: apiKey, http: hc}
}

// Error is a refusal from Sonarr or Radarr, with its explanation.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// ErrUnauthorized is returned when the API key is rejected.
var ErrUnauthorized = errors.New("API key rejected")

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v3"+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode/100 != 2:
		return &Error{Status: resp.StatusCode, Message: problem(data)}
	case out == nil:
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: unreadable response: %w", method, path, err)
	}
	return nil
}

// problem pulls the explanation out of a refusal: a list of validation errors ("errorMessage") or a
// plain message.
func problem(data []byte) string {
	var list []struct {
		ErrorMessage string `json:"errorMessage"`
	}
	if json.Unmarshal(data, &list) == nil {
		var msgs []string
		for _, e := range list {
			if e.ErrorMessage != "" {
				msgs = append(msgs, e.ErrorMessage)
			}
		}
		return strings.Join(msgs, "; ")
	}
	var one struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &one) == nil {
		return one.Message
	}
	return ""
}

// Status is the identity of an instance.
type Status struct {
	AppName string `json:"appName"`
	Version string `json:"version"`
}

// Status reads the identity of the instance and checks that it is of the expected kind.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	if err := c.do(ctx, http.MethodGet, "/system/status", nil, &s); err != nil {
		return s, err
	}
	if !strings.EqualFold(s.AppName, c.kind.Name()) {
		return s, fmt.Errorf("this address belongs to %s, not %s", s.AppName, c.kind.Name())
	}
	return s, nil
}

// Kodi metadata.

// kodiOptions are the Kodi metadata options Laterna needs: NFO files and images.
var kodiOptions = map[Kind][]string{
	Sonarr: {"seriesMetadata", "episodeMetadata", "episodeImageThumb", "seriesImages", "seasonImages", "episodeImages"},
	Radarr: {"movieMetadata", "movieImages"},
}

// resource is a configuration object (metadata, notification) kept as is: it is sent back whole
// when changed, unknown fields included.
type resource map[string]any

func (r resource) fields() []any {
	f, _ := r["fields"].([]any)
	return f
}

// field returns the named field, or nil if there is none.
func (r resource) field(name string) map[string]any {
	for _, f := range r.fields() {
		if m, ok := f.(map[string]any); ok && m["name"] == name {
			return m
		}
	}
	return nil
}

func (r resource) id() int {
	n, _ := r["id"].(float64)
	return int(n)
}

// Kodi is the state of the Kodi (XBMC) / Emby metadata of an instance.
type Kodi struct {
	Enabled bool
	// Missing holds the labels of the options Laterna needs that are turned off.
	Missing []string
}

func (c *Client) kodi(ctx context.Context) (resource, error) {
	var list []resource
	if err := c.do(ctx, http.MethodGet, "/metadata", nil, &list); err != nil {
		return nil, err
	}
	for _, r := range list {
		if r["implementation"] == "XbmcMetadata" {
			return r, nil
		}
	}
	return nil, fmt.Errorf("Kodi metadata not found on %s", c.kind.Name())
}

// Kodi reads the state of the Kodi metadata.
func (c *Client) Kodi(ctx context.Context) (Kodi, error) {
	r, err := c.kodi(ctx)
	if err != nil {
		return Kodi{}, err
	}
	k := Kodi{Enabled: r["enable"] == true}
	for _, name := range kodiOptions[c.kind] {
		if f := r.field(name); f != nil && f["value"] != true {
			label, _ := f["label"].(string)
			if label == "" {
				label = name
			}
			k.Missing = append(k.Missing, label)
		}
	}
	return k, nil
}

// EnableKodi turns Kodi metadata on with every option Laterna needs. Other settings (language, NFO
// names...) are left alone.
func (c *Client) EnableKodi(ctx context.Context) error {
	r, err := c.kodi(ctx)
	if err != nil {
		return err
	}
	r["enable"] = true
	for _, name := range kodiOptions[c.kind] {
		if f := r.field(name); f != nil {
			f["value"] = true
		}
	}
	return c.do(ctx, http.MethodPut, "/metadata/"+strconv.Itoa(r.id()), r, nil)
}

// Webhook.

// webhookEvents are the events that change files, by kind (the "on..." fields).
var webhookEvents = map[Kind][]string{
	Sonarr: {"Download", "Upgrade", "ImportComplete", "Rename", "SeriesDelete", "EpisodeFileDelete", "EpisodeFileDeleteForUpgrade"},
	Radarr: {"Download", "Upgrade", "Rename", "MovieDelete", "MovieFileDelete", "MovieFileDeleteForUpgrade"},
}

// Webhook is Laterna's webhook on an instance.
type Webhook struct {
	URL string
	// Active means at least one useful event is forwarded.
	Active bool
}

func (c *Client) webhook(ctx context.Context) (resource, error) {
	var list []resource
	if err := c.do(ctx, http.MethodGet, "/notification", nil, &list); err != nil {
		return nil, err
	}
	for _, r := range list {
		if r["implementation"] == "Webhook" && r["name"] == WebhookName {
			return r, nil
		}
	}
	return nil, nil
}

// Webhook reads Laterna's webhook; ok is false if it is not installed.
func (c *Client) Webhook(ctx context.Context) (w Webhook, ok bool, err error) {
	r, err := c.webhook(ctx)
	if err != nil || r == nil {
		return Webhook{}, false, err
	}
	if f := r.field("url"); f != nil {
		w.URL, _ = f["value"].(string)
	}
	for _, e := range webhookEvents[c.kind] {
		if r["on"+e] == true {
			w.Active = true
		}
	}
	return w, true, nil
}

// InstallWebhook installs or updates Laterna's webhook: a POST to hookURL with user:secret as Basic
// auth, on every event that changes files. The instance tries it first (a "Test" event) and refuses
// if that fails.
func (c *Client) InstallWebhook(ctx context.Context, hookURL, user, secret string) error {
	existing, err := c.webhook(ctx)
	if err != nil {
		return err
	}
	r := existing
	if r == nil {
		var schemas []resource
		if err := c.do(ctx, http.MethodGet, "/notification/schema", nil, &schemas); err != nil {
			return err
		}
		for _, s := range schemas {
			if s["implementation"] == "Webhook" {
				r = s
			}
		}
		if r == nil {
			return fmt.Errorf("%s offers no webhook", c.kind.Name())
		}
		delete(r, "presets")
		r["name"] = WebhookName
	}
	for _, e := range webhookEvents[c.kind] {
		if r["supportsOn"+e] == true {
			r["on"+e] = true
		}
	}
	values := map[string]any{"url": hookURL, "method": 1, "username": user, "password": secret}
	for name, v := range values {
		if f := r.field(name); f != nil {
			f["value"] = v
		}
	}
	if existing != nil {
		return c.do(ctx, http.MethodPut, "/notification/"+strconv.Itoa(r.id()), r, nil)
	}
	return c.do(ctx, http.MethodPost, "/notification", r, nil)
}

// RemoveWebhook removes Laterna's webhook if it is installed.
func (c *Client) RemoveWebhook(ctx context.Context) error {
	r, err := c.webhook(ctx)
	if err != nil || r == nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/notification/"+strconv.Itoa(r.id()), nil, nil)
}

// Refresh.

// Refresh asks for a refresh of every series (or movie): the instance reads its sources again and
// writes the missing NFO files and images. It returns the command ID.
func (c *Client) Refresh(ctx context.Context) (int, error) {
	name := "RefreshSeries"
	if c.kind == Radarr {
		name = "RefreshMovie"
	}
	var cmd struct {
		ID int `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/command", map[string]any{"name": name}, &cmd)
	return cmd.ID, err
}

// Command reads the state of a command: done once it has finished, failed if it failed.
func (c *Client) Command(ctx context.Context, id int) (done bool, failed string, err error) {
	var cmd struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := c.do(ctx, http.MethodGet, "/command/"+strconv.Itoa(id), nil, &cmd); err != nil {
		return false, "", err
	}
	switch cmd.Status {
	case "completed":
		return true, "", nil
	case "failed", "aborted", "cancelled", "orphaned":
		return true, cmd.Status + " " + cmd.Message, nil
	}
	return false, "", nil
}

// Tracked folders.

// Folder is a tracked series or movie, with the folder the instance keeps it in.
type Folder struct {
	Title string
	// Path is the folder as the instance sees it ("/tv/Animes/Dr. STONE").
	Path string
	// File is the file of a movie, relative to its folder ("" if there is none). Empty for a
	// series.
	File string
	// HasFiles means at least one file is present.
	HasFiles bool
}

// Folders lists the tracked series (Sonarr) or movies (Radarr).
func (c *Client) Folders(ctx context.Context) ([]Folder, error) {
	if c.kind == Radarr {
		var movies []struct {
			Title     string `json:"title"`
			Path      string `json:"path"`
			HasFile   bool   `json:"hasFile"`
			MovieFile *struct {
				RelativePath string `json:"relativePath"`
			} `json:"movieFile"`
		}
		if err := c.do(ctx, http.MethodGet, "/movie", nil, &movies); err != nil {
			return nil, err
		}
		out := make([]Folder, len(movies))
		for i, m := range movies {
			out[i] = Folder{Title: m.Title, Path: m.Path, HasFiles: m.HasFile}
			if m.MovieFile != nil {
				out[i].File = m.MovieFile.RelativePath
			}
		}
		return out, nil
	}
	var series []struct {
		Title      string `json:"title"`
		Path       string `json:"path"`
		Statistics struct {
			EpisodeFileCount int `json:"episodeFileCount"`
		} `json:"statistics"`
	}
	if err := c.do(ctx, http.MethodGet, "/series", nil, &series); err != nil {
		return nil, err
	}
	out := make([]Folder, len(series))
	for i, s := range series {
		out[i] = Folder{Title: s.Title, Path: s.Path, HasFiles: s.Statistics.EpisodeFileCount > 0}
	}
	return out, nil
}

// ValidURL checks the address of an instance (http or https, no query).
func ValidURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" {
		return fmt.Errorf("invalid address %q (\"http://host:port\" expected)", s)
	}
	return nil
}
