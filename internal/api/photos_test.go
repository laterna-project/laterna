package api

import (
	"bytes"
	"image"
	"io"
	"net/http"
	"testing"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/domain"
)

// Photos over HTTP: timeline, months, albums, EXIF details, a photo resized and rotated by the
// image route.
func TestPhotosOverHTTP(t *testing.T) {
	base, token := libraryServer(t, domain.LibraryPhotos, "Photos")
	ctx := t.Context()
	photos := laternav1connect.NewPhotoServiceClient(http.DefaultClient, base)

	list, err := photos.ListPhotos(ctx, authed(&laternav1.ListPhotosRequest{PageSize: 3}, token))
	if err != nil || len(list.Msg.GetPhotos()) != 3 || list.Msg.GetTotalSize() != 4 || list.Msg.GetNextPageToken() == "" {
		t.Fatalf("timeline: %v %v", list, err)
	}
	var turned *laternav1.PhotoSummary
	for _, p := range list.Msg.GetPhotos() {
		if p.GetTitle() == "IMG_0002" {
			turned = p
		}
	}
	if turned == nil || turned.GetWidth() != 480 || turned.GetHeight() != 640 || turned.GetUtcOffset() != nil || len(turned.GetImages()) != 1 ||
		turned.GetImages()[0].GetKind() != laternav1.ImageKind_IMAGE_KIND_PHOTO {
		t.Fatalf("rotated photo: %v", turned)
	}
	// The resized photo, rotated.
	resp := get(t, base+turned.GetImages()[0].GetUrl()+"?w=200")
	body, _ := io.ReadAll(resp.Body)
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(body)); err != nil || cfg.Width != 240 || cfg.Height != 320 {
		t.Errorf("resized photo: %+v %v", cfg, err)
	}

	next, err := photos.ListPhotos(ctx, authed(&laternav1.ListPhotosRequest{PageSize: 3, PageToken: list.Msg.GetNextPageToken()}, token))
	if err != nil || len(next.Msg.GetPhotos()) != 1 || next.Msg.GetNextPageToken() != "" {
		t.Fatalf("next page: %v %v", next, err)
	}
	months, err := photos.ListPhotoMonths(ctx, authed(&laternav1.ListPhotoMonthsRequest{}, token))
	if err != nil || len(months.Msg.GetMonths()) != 3 || months.Msg.GetMonths()[1].GetCount() != 2 {
		t.Errorf("months: %v %v", months, err)
	}
	albums, err := photos.ListPhotoAlbums(ctx, authed(&laternav1.ListPhotoAlbumsRequest{}, token))
	if err != nil || len(albums.Msg.GetAlbums()) != 2 {
		t.Fatalf("albums: %v %v", albums, err)
	}
	y2024, err := photos.GetPhotoAlbum(ctx, authed(&laternav1.GetPhotoAlbumRequest{AlbumId: albums.Msg.GetAlbums()[0].GetId()}, token))
	if err != nil || len(y2024.Msg.GetAlbums()) != 1 || y2024.Msg.GetAlbum().GetAlbumCount() != 1 || len(y2024.Msg.GetAlbum().GetImages()) != 1 {
		t.Fatalf("album 2024: %v %v", y2024, err)
	}
	vacances := y2024.Msg.GetAlbums()[0]
	in, err := photos.ListPhotos(ctx, authed(&laternav1.ListPhotosRequest{AlbumId: vacances.GetId()}, token))
	if err != nil || len(in.Msg.GetPhotos()) != 2 {
		t.Fatalf("photos of Vacances: %v %v", in, err)
	}
	var first string
	for _, p := range in.Msg.GetPhotos() {
		if p.GetTitle() == "IMG_0001" {
			first = p.GetId()
		}
	}
	full, err := photos.GetPhoto(ctx, authed(&laternav1.GetPhotoRequest{PhotoId: first}, token))
	if err != nil {
		t.Fatal(err)
	}
	ph := full.Msg.GetPhoto()
	if ph.GetCameraModel() != "Appareil Test" || ph.GetFNumber() != 1.8 || ph.GetExposureTime() != "1/250" || ph.GetIso() != 100 ||
		ph.GetLocation().GetLatitude() < 48 || ph.GetSummary().GetUtcOffset().AsDuration().Hours() != 2 || len(full.Msg.GetFiles()) != 1 {
		t.Errorf("details: %v", ph)
	}
}
