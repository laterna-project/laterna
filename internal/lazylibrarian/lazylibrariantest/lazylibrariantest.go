// Package lazylibrariantest fakes LazyLibrarian for tests: its version, book searches (OpenLibrary
// as it answers through LazyLibrarian), the books it knows and what wanting one does.
package lazylibrariantest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Key is the API key the fake accepts.
const Key = "test-key"

// Entry is a book a search can find.
type Entry struct {
	ID     string
	Title  string
	Author string
	ISBN   string
	Year   int
	// Cover as the source gives it (an OpenLibrary thumbnail, "...-S.jpg").
	Cover string
}

// Server is a fake LazyLibrarian.
type Server struct {
	*httptest.Server

	mu sync.Mutex
	// Catalog is what a search finds.
	Catalog []Entry
	// Status of each book the instance knows, by ID: "Skipped", "Wanted", "Snatched", "Have"...
	Status map[string]string
	// Commands lists the commands received ("addBook OL1W"), in order.
	Commands []string
}

// New starts a fake LazyLibrarian that is stopped when the test ends.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{Status: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := r.URL.Query()
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.URL.Path != "/api" {
		http.NotFound(w, r)
		return
	}
	if q.Get("apikey") != Key {
		write(map[string]any{"Success": false, "Data": "", "Error": map[string]any{"Code": 401, "Message": "Incorrect API key"}})
		return
	}
	id := q.Get("id")
	switch cmd := q.Get("cmd"); cmd {
	case "getVersion":
		write(map[string]any{"Success": true, "install_type": "source DOCKER", "current_version": "8006a0fc"})
	case "findBook":
		term := strings.ToLower(q.Get("name"))
		list := []any{}
		for _, e := range s.Catalog {
			if strings.Contains(strings.ToLower(e.Title+" "+e.Author), term) {
				list = append(list, map[string]any{
					"bookid": e.ID, "bookname": e.Title, "authorname": e.Author, "bookisbn": nilIfEmpty(e.ISBN),
					"bookimg": e.Cover, "bookdate": "", "bookpub": e.Year, "bookdesc": "About " + e.Title,
					"highest_fuzz": 90, "source": "OpenLibrary",
				})
			}
		}
		write(list)
	case "addBook":
		s.Commands = append(s.Commands, cmd+" "+id)
		if _, ok := s.Status[id]; !ok && s.entry(id) != nil {
			s.Status[id] = "Skipped"
		}
		_, _ = w.Write([]byte("OK"))
	case "queueBook", "searchBook":
		s.Commands = append(s.Commands, cmd+" "+id+" "+q.Get("type"))
		if _, ok := s.Status[id]; !ok {
			_, _ = w.Write([]byte("Invalid id"))
			return
		}
		if cmd == "queueBook" {
			s.Status[id] = "Wanted"
		}
		_, _ = w.Write([]byte("OK"))
	case "getAllBooks":
		list := []any{}
		for id, status := range s.Status {
			e := s.entry(id)
			if e == nil {
				continue
			}
			list = append(list, map[string]any{
				"BookID": id, "BookName": e.Title, "AuthorName": e.Author, "BookIsbn": nilIfEmpty(e.ISBN),
				"BookImg": "cache/book/" + id + ".jpg", "Status": status, "AudioStatus": "Skipped",
			})
		}
		write(list)
	default:
		_, _ = w.Write([]byte("Unknown command"))
	}
}

func (s *Server) entry(id string) *Entry {
	for i := range s.Catalog {
		if s.Catalog[i].ID == id {
			return &s.Catalog[i]
		}
	}
	return nil
}

func nilIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// Set changes the state under the lock.
func (s *Server) Set(f func(s *Server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// Get reads the state under the lock.
func (s *Server) Get(f func(s *Server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}
