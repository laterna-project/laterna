package rpc

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// Domain types to contract messages.

func accountMsg(a domain.Account) *laternav1.Account {
	libs := &laternav1.LibraryAccess{All: a.IsAdmin || a.Libraries.All}
	if !libs.GetAll() {
		for _, id := range a.Libraries.IDs {
			libs.LibraryIds = append(libs.LibraryIds, id.String())
		}
	}
	return &laternav1.Account{
		Id: a.ID.String(), Username: a.Username, IsAdmin: a.IsAdmin, CreatedAt: timestamppb.New(a.CreatedAt),
		Disabled: a.Disabled, Libraries: libs, Parental: parentalMsg(a.Parental), DenyDownloads: a.DenyDownloads,
	}
}

func profileMsg(p domain.Profile) *laternav1.Profile {
	return &laternav1.Profile{
		Id: p.ID.String(), Name: p.Name, HasPin: p.HasPIN, Kid: p.Kid, CreatedAt: timestamppb.New(p.CreatedAt),
		Parental: parentalMsg(p.Parental), Language: p.Language,
	}
}

func parentalMsg(c domain.ParentalControl) *laternav1.ParentalControl {
	msg := &laternav1.ParentalControl{BlockUnrated: c.BlockUnrated}
	if c.MaxAge != nil {
		age := clampInt32(*c.MaxAge)
		msg.MaxAge = &age
	}
	return msg
}

// parentalFromMsg converts a parental control; nil if the message has none.
func parentalFromMsg(m *laternav1.ParentalControl) *domain.ParentalControl {
	if m == nil {
		return nil
	}
	c := &domain.ParentalControl{BlockUnrated: m.GetBlockUnrated()}
	if m.MaxAge != nil {
		age := int(m.GetMaxAge())
		c.MaxAge = &age
	}
	return c
}

// libraryAccessFromMsg converts allowed libraries; nil if the message has none.
func libraryAccessFromMsg(m *laternav1.LibraryAccess) (*domain.LibraryAccess, error) {
	if m == nil {
		return nil, nil
	}
	access := &domain.LibraryAccess{All: m.GetAll()}
	for _, raw := range m.GetLibraryIds() {
		id, err := parseID(raw, "library_ids")
		if err != nil {
			return nil, err
		}
		access.IDs = append(access.IDs, id)
	}
	return access, nil
}

func sessionMsg(d app.SessionDetails, current domain.ID) *laternav1.Session {
	s := d.Session
	msg := &laternav1.Session{
		Id:      s.ID.String(),
		Account: accountMsg(d.Account),
		Device: &laternav1.Device{
			Name: s.Device.Name, Client: s.Device.Client, ClientVersion: s.Device.ClientVersion, Platform: s.Device.Platform,
		},
		CreatedAt:  timestamppb.New(s.CreatedAt),
		LastUsedAt: timestamppb.New(s.LastUsedAt),
		ExpiresAt:  timestamppb.New(s.ExpiresAt),
		Current:    s.ID == current,
	}
	if d.Profile != nil {
		msg.Profile = profileMsg(*d.Profile)
	}
	return msg
}

func deviceFromMsg(d *laternav1.Device) domain.Device {
	return domain.Device{
		Name: d.GetName(), Client: d.GetClient(), ClientVersion: d.GetClientVersion(), Platform: d.GetPlatform(),
	}
}

// parseID reads an ID supplied by the client.
func parseID(s, what string) (domain.ID, error) {
	id, err := domain.ParseID(s)
	if err != nil {
		return domain.ID{}, domain.Invalid("request.invalid_id", "field", what)
	}
	return id, nil
}

func libraryMsg(l domain.Library) *laternav1.Library {
	msg := &laternav1.Library{
		Id: l.ID.String(), Name: l.Name, Kind: libraryKindMsg(l.Kind), Paths: l.Paths, Language: l.Language,
		CreatedAt: timestamppb.New(l.CreatedAt), UpdatedAt: timestamppb.New(l.UpdatedAt),
	}
	if l.LastScanAt != nil {
		msg.LastScanAt = timestamppb.New(*l.LastScanAt)
	}
	return msg
}

func libraryKindMsg(k domain.LibraryKind) laternav1.LibraryKind {
	switch k {
	case domain.LibraryMovies:
		return laternav1.LibraryKind_LIBRARY_KIND_MOVIES
	case domain.LibraryShows:
		return laternav1.LibraryKind_LIBRARY_KIND_SHOWS
	case domain.LibraryMusic:
		return laternav1.LibraryKind_LIBRARY_KIND_MUSIC
	case domain.LibraryBooks:
		return laternav1.LibraryKind_LIBRARY_KIND_BOOKS
	case domain.LibraryPhotos:
		return laternav1.LibraryKind_LIBRARY_KIND_PHOTOS
	}
	return laternav1.LibraryKind_LIBRARY_KIND_UNSPECIFIED
}

// libraryKindFromMsg returns "" for a missing or unknown kind: app rejects it.
func libraryKindFromMsg(k laternav1.LibraryKind) domain.LibraryKind {
	switch k {
	case laternav1.LibraryKind_LIBRARY_KIND_MOVIES:
		return domain.LibraryMovies
	case laternav1.LibraryKind_LIBRARY_KIND_SHOWS:
		return domain.LibraryShows
	case laternav1.LibraryKind_LIBRARY_KIND_MUSIC:
		return domain.LibraryMusic
	case laternav1.LibraryKind_LIBRARY_KIND_BOOKS:
		return domain.LibraryBooks
	case laternav1.LibraryKind_LIBRARY_KIND_PHOTOS:
		return domain.LibraryPhotos
	case laternav1.LibraryKind_LIBRARY_KIND_UNSPECIFIED:
	}
	return ""
}

func itemCountsMsg(c map[domain.ItemKind]int) *laternav1.ItemCounts {
	return &laternav1.ItemCounts{
		Movies: clampInt32(c[domain.ItemMovie]), Series: clampInt32(c[domain.ItemSeries]),
		Seasons: clampInt32(c[domain.ItemSeason]), Episodes: clampInt32(c[domain.ItemEpisode]),
		Artists: clampInt32(c[domain.ItemArtist]), Albums: clampInt32(c[domain.ItemAlbum]), Tracks: clampInt32(c[domain.ItemTrack]),
		BookSeries: clampInt32(c[domain.ItemBookSeries]), Books: clampInt32(c[domain.ItemBook]),
		PhotoAlbums: clampInt32(c[domain.ItemPhotoAlbum]), Photos: clampInt32(c[domain.ItemPhoto]),
	}
}
