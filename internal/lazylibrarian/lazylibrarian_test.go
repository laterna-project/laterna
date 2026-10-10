package lazylibrarian_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/lazylibrarian"
	"github.com/laterna-project/laterna/internal/lazylibrarian/lazylibrariantest"
)

func TestClient(t *testing.T) {
	ctx := context.Background()
	srv := lazylibrariantest.New(t)
	srv.Set(func(s *lazylibrariantest.Server) {
		s.Catalog = []lazylibrariantest.Entry{
			{ID: "OL893414W", Title: "Dune", Author: "Frank Herbert", Year: 1965, Cover: "http://covers.openlibrary.org/b/id/11481354-S.jpg"},
			{ID: "OL45804W", Title: "Fantastic Mr Fox", Author: "Roald Dahl", ISBN: "9780140328721"},
		}
	})
	c := lazylibrarian.New(srv.URL+"/", lazylibrariantest.Key, srv.Client())
	if v, err := c.Version(ctx); err != nil || v != "8006a0fc" {
		t.Fatalf("version: %q %v", v, err)
	}
	if _, err := lazylibrarian.New(srv.URL, "wrong", srv.Client()).Version(ctx); !errors.Is(err, lazylibrarian.ErrUnauthorized) {
		t.Errorf("wrong key: %v", err)
	}

	found, err := c.Search(ctx, "dune")
	if err != nil || len(found) != 1 {
		t.Fatalf("search: %+v %v", found, err)
	}
	if b := found[0]; b.ID != "OL893414W" || b.Author != "Frank Herbert" || b.Year != 1965 ||
		b.Cover != "https://covers.openlibrary.org/b/id/11481354-L.jpg" || b.Status != "" {
		t.Errorf("result: %+v", b)
	}

	// Wanting a book: added, ebook wanted, then searched.
	if err := c.Want(ctx, "OL45804W"); err != nil {
		t.Fatal(err)
	}
	srv.Get(func(s *lazylibrariantest.Server) {
		want := []string{"addBook OL45804W", "queueBook OL45804W eBook", "searchBook OL45804W eBook"}
		if !slices.Equal(s.Commands, want) {
			t.Errorf("commands: %v", s.Commands)
		}
	})
	books, err := c.Books(ctx)
	if err != nil || len(books) != 1 || !books[0].Wanted() || books[0].ISBN != "9780140328721" || books[0].Cover != "" {
		t.Fatalf("books: %+v %v", books, err)
	}
	srv.Set(func(s *lazylibrariantest.Server) { s.Status["OL45804W"] = "Have" })
	if books, _ := c.Books(ctx); len(books) != 1 || !books[0].InLibrary() {
		t.Errorf("in library: %+v", books)
	}
	if err := c.Want(ctx, "OL-unknown"); err == nil {
		t.Error("unknown book wanted")
	}
}
