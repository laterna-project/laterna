package rpc

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// PhotoService implements laterna.v1.PhotoService.
type PhotoService struct {
	app *app.App
}

func photoAlbumMsg(v domain.ItemView) *laternav1.PhotoAlbum {
	it := v.Item
	msg := &laternav1.PhotoAlbum{
		Id: it.ID.String(), LibraryId: it.LibraryID.String(), Title: it.Title, Images: imagesMsg(v.Images),
		PhotoCount: clampInt32(v.PhotoCount), AlbumCount: clampInt32(v.AlbumCount), UserData: userDataMsg(v.UserData),
		AddedAt: timestamppb.New(it.AddedAt),
	}
	if it.ParentID != nil {
		msg.ParentId = it.ParentID.String()
	}
	return msg
}

func photoSummaryMsg(v domain.ItemView) *laternav1.PhotoSummary {
	it := v.Item
	msg := &laternav1.PhotoSummary{
		Id: it.ID.String(), LibraryId: it.LibraryID.String(), Title: it.Title, Images: imagesMsg(v.Images),
		UserData: userDataMsg(v.UserData), AddedAt: timestamppb.New(it.AddedAt),
	}
	if it.ParentID != nil {
		msg.AlbumId = it.ParentID.String()
	}
	if p := v.Photo; p != nil {
		msg.TakenAt, msg.Width, msg.Height = timestamppb.New(p.TakenAt), clampInt32(p.Width), clampInt32(p.Height)
		if p.UTCOffset != nil {
			msg.UtcOffset = durationpb.New(time.Duration(*p.UTCOffset) * time.Minute)
		}
	}
	return msg
}

func photosMsg(views []domain.ItemView) []*laternav1.PhotoSummary {
	out := make([]*laternav1.PhotoSummary, len(views))
	for i, v := range views {
		out[i] = photoSummaryMsg(v)
	}
	return out
}

func photoQueryOf(libraryID, albumID string, favorites bool) (app.PhotoQuery, error) {
	lib, err := parseOptionalID(libraryID, "library_id")
	if err != nil {
		return app.PhotoQuery{}, err
	}
	album, err := parseOptionalID(albumID, "album_id")
	if err != nil {
		return app.PhotoQuery{}, err
	}
	return app.PhotoQuery{LibraryID: lib, AlbumID: album, FavoritesOnly: favorites}, nil
}

// ListPhotos returns a page of photos.
func (s *PhotoService) ListPhotos(ctx context.Context, req *connect.Request[laternav1.ListPhotosRequest]) (*connect.Response[laternav1.ListPhotosResponse], error) {
	m := req.Msg
	pq, err := photoQueryOf(m.GetLibraryId(), m.GetAlbumId(), m.GetFavoritesOnly())
	if err != nil {
		return nil, err
	}
	pq.PageSize, pq.PageToken = int(m.GetPageSize()), m.GetPageToken()
	page, err := s.app.ListPhotos(ctx, principal(ctx), pq)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ListPhotosResponse{
		Photos: photosMsg(page.Items), NextPageToken: page.NextPageToken, TotalSize: clampInt32(page.Total),
	}), nil
}

// ListPhotoMonths counts the photos of each month.
func (s *PhotoService) ListPhotoMonths(ctx context.Context, req *connect.Request[laternav1.ListPhotoMonthsRequest]) (*connect.Response[laternav1.ListPhotoMonthsResponse], error) {
	m := req.Msg
	pq, err := photoQueryOf(m.GetLibraryId(), m.GetAlbumId(), m.GetFavoritesOnly())
	if err != nil {
		return nil, err
	}
	months, err := s.app.PhotoMonths(ctx, principal(ctx), pq)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListPhotoMonthsResponse{}
	for _, mo := range months {
		resp.Months = append(resp.Months, &laternav1.PhotoMonth{Year: clampInt32(mo.Year), Month: clampInt32(mo.Month), Count: clampInt32(mo.Count)})
	}
	return connect.NewResponse(resp), nil
}

// ListPhotoAlbums lists the top-level albums.
func (s *PhotoService) ListPhotoAlbums(ctx context.Context, req *connect.Request[laternav1.ListPhotoAlbumsRequest]) (*connect.Response[laternav1.ListPhotoAlbumsResponse], error) {
	lib, err := parseOptionalID(req.Msg.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	albums, err := s.app.PhotoAlbums(ctx, principal(ctx), lib)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListPhotoAlbumsResponse{}
	for _, a := range albums {
		resp.Albums = append(resp.Albums, photoAlbumMsg(a))
	}
	return connect.NewResponse(resp), nil
}

// GetPhotoAlbum returns an album and the albums inside it.
func (s *PhotoService) GetPhotoAlbum(ctx context.Context, req *connect.Request[laternav1.GetPhotoAlbumRequest]) (*connect.Response[laternav1.GetPhotoAlbumResponse], error) {
	id, err := parseID(req.Msg.GetAlbumId(), "album_id")
	if err != nil {
		return nil, err
	}
	v, sub, err := s.app.PhotoAlbum(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.GetPhotoAlbumResponse{Album: photoAlbumMsg(v)}
	for _, a := range sub {
		resp.Albums = append(resp.Albums, photoAlbumMsg(a))
	}
	return connect.NewResponse(resp), nil
}

// GetPhoto returns the details of a photo.
func (s *PhotoService) GetPhoto(ctx context.Context, req *connect.Request[laternav1.GetPhotoRequest]) (*connect.Response[laternav1.GetPhotoResponse], error) {
	id, err := parseID(req.Msg.GetPhotoId(), "photo_id")
	if err != nil {
		return nil, err
	}
	v, d, err := s.app.Photo(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	msg := &laternav1.Photo{Summary: photoSummaryMsg(v)}
	if p := v.Photo; p != nil {
		msg.CameraMake, msg.CameraModel, msg.Lens = p.Make, p.Model, p.Lens
		msg.FNumber, msg.FocalLength, msg.ExposureTime, msg.Iso = p.FNumber, p.FocalLength, p.ExposureTime, clampInt32(p.ISO)
		if p.Latitude != nil && p.Longitude != nil {
			msg.Location = &laternav1.GeoPoint{Latitude: *p.Latitude, Longitude: *p.Longitude}
		}
	}
	return connect.NewResponse(&laternav1.GetPhotoResponse{Photo: msg, Files: filesMsg(d.Files)}), nil
}
