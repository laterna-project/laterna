package store

import (
	"context"
	"errors"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/migrations"
)

// Migration 00029 rebuilds the request tables to widen their kinds (music and books): requests keep
// their destination, and the open request of a title stays unique, by number or by key.
func TestRequestMigrationKeepsRequests(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	shows, music := newLibrary("Shows", domain.LibraryShows, "/s"), newLibrary("Music", domain.LibraryMusic, "/m")
	account := newAccount("Léa", false)
	profile := domain.Profile{ID: domain.NewID(), AccountID: account.ID, Name: "Léa", CreatedAt: t0, UpdatedAt: t0}
	series := domain.RequestDestination{ID: domain.NewID(), Name: "Series", Kind: domain.RequestSeries, LibraryID: shows.ID, RootFolder: "/tv", QualityProfileID: 1, SeriesType: domain.SeriesStandard, CreatedAt: t0, UpdatedAt: t0}
	request := domain.MediaRequest{
		ID: domain.NewID(), Kind: domain.RequestSeries, ExternalID: 101, Title: "Frieren", Status: domain.RequestPending,
		Seasons: domain.SeasonsAll, Destination: &series, AccountID: account.ID, ProfileID: profile.ID, CreatedAt: t0, UpdatedAt: t0,
	}
	mustWrite(t, st, func(q Q) error {
		return errors.Join(q.CreateLibrary(ctx, shows), q.CreateLibrary(ctx, music), q.CreateAccount(ctx, account, "hash"),
			q.CreateProfile(ctx, profile, ""), q.CreateRequestDestination(ctx, series), q.CreateRequest(ctx, request))
	})

	provider, err := goose.NewProvider(database.DialectSQLite3, st.writer, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 28); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var fk int
	if err := st.writer.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign keys: %d %v", fk, err)
	}
	got, err := st.Read().Request(ctx, request.ID)
	if err != nil || got.Destination == nil || got.Destination.ID != series.ID || got.ExternalID != 101 {
		t.Fatalf("request after the rebuild: %+v %v", got, err)
	}

	// Music by key: one open request per album, whatever its number.
	albums := domain.RequestDestination{ID: domain.NewID(), Name: "Music", Kind: domain.RequestMusic, LibraryID: music.ID, RootFolder: "/music", QualityProfileID: 2, MetadataProfileID: 1, MetadataProfileName: "Standard", SeriesType: domain.SeriesStandard, CreatedAt: t0, UpdatedAt: t0}
	album := domain.MediaRequest{
		ID: domain.NewID(), Kind: domain.RequestAlbum, ExternalKey: "rg-discovery", Title: "Discovery", Subtitle: "Daft Punk",
		Status: domain.RequestPending, Seasons: domain.SeasonsAll, Destination: &albums, AccountID: account.ID, ProfileID: profile.ID,
		CreatedAt: t0, UpdatedAt: t0,
	}
	mustWrite(t, st, func(q Q) error {
		return errors.Join(q.CreateRequestDestination(ctx, albums), q.CreateRequest(ctx, album))
	})
	again := album
	again.ID = domain.NewID()
	if err := st.Write(ctx, func(q Q) error { return q.CreateRequest(ctx, again) }); !errors.Is(err, ErrDuplicate) {
		t.Errorf("album requested twice: %v", err)
	}
	open, err := st.Read().OpenRequests(ctx, domain.RequestAlbum, []string{"rg-discovery", "rg-other"})
	if err != nil || len(open) != 1 || open["rg-discovery"] != album.ID {
		t.Errorf("open requests by key: %v %v", open, err)
	}
	if open, _ := st.Read().OpenRequests(ctx, domain.RequestSeries, []string{"101"}); open["101"] != request.ID {
		t.Errorf("open requests by number: %v", open)
	}
	if got, err := st.Read().Request(ctx, album.ID); err != nil || got.Subtitle != "Daft Punk" || got.Destination.MetadataProfileName != "Standard" {
		t.Errorf("album request: %+v %v", got, err)
	}

	// Going back forgets music and books, and keeps the rest.
	if _, err := provider.DownTo(ctx, 28); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.writer.QueryRowContext(ctx, "SELECT count(*) FROM requests").Scan(&n); err != nil || n != 1 {
		t.Errorf("requests after going back: %d %v", n, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
}
