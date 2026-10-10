package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// CatalogService implements laterna.v1.CatalogService.
type CatalogService struct {
	app *app.App
}

// ListCatalogLibraries lists the libraries to browse.
func (s *CatalogService) ListCatalogLibraries(ctx context.Context, _ *connect.Request[laternav1.ListCatalogLibrariesRequest]) (*connect.Response[laternav1.ListCatalogLibrariesResponse], error) {
	list, err := s.app.CatalogLibraries(ctx, principal(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.CatalogLibrary, len(list))
	for i, l := range list {
		out[i] = &laternav1.CatalogLibrary{
			Id: l.Library.ID.String(), Name: l.Library.Name, Kind: libraryKindMsg(l.Library.Kind), Counts: itemCountsMsg(l.Counts),
		}
	}
	return connect.NewResponse(&laternav1.ListCatalogLibrariesResponse{Libraries: out}), nil
}

// ListMovies returns a page of movies.
func (s *CatalogService) ListMovies(ctx context.Context, req *connect.Request[laternav1.ListMoviesRequest]) (*connect.Response[laternav1.ListMoviesResponse], error) {
	m := req.Msg
	lib, err := parseOptionalID(m.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	page, err := s.app.ListMovies(ctx, principal(ctx), app.ListQuery{
		LibraryID: lib, Sort: itemSortFromMsg(m.GetSort()), Reverse: m.GetReverse(), Genre: m.GetGenre(),
		Played: m.Played, FavoritesOnly: m.GetFavoritesOnly(), PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.MovieSummary, len(page.Items))
	for i, v := range page.Items {
		out[i] = movieSummaryMsg(v)
	}
	return connect.NewResponse(&laternav1.ListMoviesResponse{
		Movies: out, NextPageToken: page.NextPageToken, TotalSize: clampInt32(page.Total),
	}), nil
}

// GetMovie returns the details of a movie.
func (s *CatalogService) GetMovie(ctx context.Context, req *connect.Request[laternav1.GetMovieRequest]) (*connect.Response[laternav1.GetMovieResponse], error) {
	id, err := parseID(req.Msg.GetMovieId(), "movie_id")
	if err != nil {
		return nil, err
	}
	v, d, err := s.app.Movie(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	it := v.Item
	return connect.NewResponse(&laternav1.GetMovieResponse{
		Movie: &laternav1.Movie{
			Id: it.ID.String(), LibraryId: it.LibraryID.String(), Title: it.Title, OriginalTitle: it.OriginalTitle,
			Year: clampInt32(it.Year), PremiereDate: it.PremiereDate, Overview: it.Overview, Tagline: it.Tagline,
			OfficialRating: it.OfficialRating, CommunityRating: it.CommunityRating, Runtime: durationMsg(it.Runtime),
			Genres: d.Genres, Studios: d.Studios, Credits: creditsMsg(d.Credits), ProviderIds: d.ProviderIDs, Collections: collectionRefsMsg(d.Collections),
			Images: imagesMsg(v.Images), UserData: userDataMsg(v.UserData), AddedAt: timestamppb.New(it.AddedAt),
		},
		Files: filesMsg(d.Files),
	}), nil
}

// ListSeries returns a page of series.
func (s *CatalogService) ListSeries(ctx context.Context, req *connect.Request[laternav1.ListSeriesRequest]) (*connect.Response[laternav1.ListSeriesResponse], error) {
	m := req.Msg
	lib, err := parseOptionalID(m.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	page, err := s.app.ListSeries(ctx, principal(ctx), app.ListQuery{
		LibraryID: lib, Sort: itemSortFromMsg(m.GetSort()), Reverse: m.GetReverse(), Genre: m.GetGenre(),
		FavoritesOnly: m.GetFavoritesOnly(), PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.SeriesSummary, len(page.Items))
	for i, v := range page.Items {
		out[i] = seriesSummaryMsg(v)
	}
	return connect.NewResponse(&laternav1.ListSeriesResponse{
		Series: out, NextPageToken: page.NextPageToken, TotalSize: clampInt32(page.Total),
	}), nil
}

// GetSeries returns the details of a series and its seasons.
func (s *CatalogService) GetSeries(ctx context.Context, req *connect.Request[laternav1.GetSeriesRequest]) (*connect.Response[laternav1.GetSeriesResponse], error) {
	id, err := parseID(req.Msg.GetSeriesId(), "series_id")
	if err != nil {
		return nil, err
	}
	v, d, seasons, err := s.app.Series(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	it := v.Item
	out := make([]*laternav1.Season, len(seasons))
	for i, sv := range seasons {
		out[i] = seasonMsg(ctx, sv)
	}
	return connect.NewResponse(&laternav1.GetSeriesResponse{
		Series: &laternav1.Series{
			Id: it.ID.String(), LibraryId: it.LibraryID.String(), Title: it.Title, OriginalTitle: it.OriginalTitle,
			Year: clampInt32(it.Year), PremiereDate: it.PremiereDate, Overview: it.Overview, Tagline: it.Tagline,
			OfficialRating: it.OfficialRating, CommunityRating: it.CommunityRating,
			Genres: d.Genres, Studios: d.Studios, Credits: creditsMsg(d.Credits), ProviderIds: d.ProviderIDs, Collections: collectionRefsMsg(d.Collections),
			Images: imagesMsg(v.Images), EpisodeCount: clampInt32(v.EpisodeCount), UnplayedCount: clampInt32(v.UnplayedCount),
			UserData: userDataMsg(v.UserData), AddedAt: timestamppb.New(it.AddedAt),
		},
		Seasons: out,
	}), nil
}

// ListEpisodes lists the episodes of a series or a season.
func (s *CatalogService) ListEpisodes(ctx context.Context, req *connect.Request[laternav1.ListEpisodesRequest]) (*connect.Response[laternav1.ListEpisodesResponse], error) {
	seriesID, err := parseID(req.Msg.GetSeriesId(), "series_id")
	if err != nil {
		return nil, err
	}
	seasonID, err := parseOptionalID(req.Msg.GetSeasonId(), "season_id")
	if err != nil {
		return nil, err
	}
	eps, err := s.app.Episodes(ctx, principal(ctx), seriesID, seasonID)
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.Episode, len(eps))
	for i, v := range eps {
		out[i] = episodeMsg(ctx, v)
	}
	return connect.NewResponse(&laternav1.ListEpisodesResponse{Episodes: out}), nil
}

// GetEpisode returns the details of an episode.
func (s *CatalogService) GetEpisode(ctx context.Context, req *connect.Request[laternav1.GetEpisodeRequest]) (*connect.Response[laternav1.GetEpisodeResponse], error) {
	id, err := parseID(req.Msg.GetEpisodeId(), "episode_id")
	if err != nil {
		return nil, err
	}
	v, d, err := s.app.Episode(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.GetEpisodeResponse{
		Episode: episodeMsg(ctx, v), Files: filesMsg(d.Files), Credits: creditsMsg(d.Credits),
	}), nil
}

// ListGenres lists the genres.
func (s *CatalogService) ListGenres(ctx context.Context, req *connect.Request[laternav1.ListGenresRequest]) (*connect.Response[laternav1.ListGenresResponse], error) {
	lib, err := parseOptionalID(req.Msg.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	genres, err := s.app.Genres(ctx, principal(ctx), lib)
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.Genre, len(genres))
	for i, g := range genres {
		out[i] = &laternav1.Genre{Name: g.Name, Count: clampInt32(g.Count)}
	}
	return connect.NewResponse(&laternav1.ListGenresResponse{Genres: out}), nil
}

// Search searches the catalog.
func (s *CatalogService) Search(ctx context.Context, req *connect.Request[laternav1.SearchRequest]) (*connect.Response[laternav1.SearchResponse], error) {
	views, err := s.app.Search(ctx, principal(ctx), req.Msg.GetQuery(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.SearchResult, 0, len(views))
	for _, v := range views {
		if msg := searchResultMsg(ctx, v); msg != nil {
			out = append(out, msg)
		}
	}
	return connect.NewResponse(&laternav1.SearchResponse{Results: out}), nil
}

// searchResultMsg is an item of any kind a list can show, as a list shows it; nil for the others (a
// season, a photo).
func searchResultMsg(ctx context.Context, v domain.ItemView) *laternav1.SearchResult {
	switch v.Item.Kind {
	case domain.ItemMovie:
		return &laternav1.SearchResult{Item: &laternav1.SearchResult_Movie{Movie: movieSummaryMsg(v)}}
	case domain.ItemSeries:
		return &laternav1.SearchResult{Item: &laternav1.SearchResult_Series{Series: seriesSummaryMsg(v)}}
	case domain.ItemEpisode:
		return &laternav1.SearchResult{Item: &laternav1.SearchResult_Episode{Episode: episodeMsg(ctx, v)}}
	case domain.ItemArtist:
		return &laternav1.SearchResult{Item: &laternav1.SearchResult_Artist{Artist: artistSummaryMsg(ctx, v)}}
	case domain.ItemAlbum:
		return &laternav1.SearchResult{Item: &laternav1.SearchResult_Album{Album: albumSummaryMsg(ctx, v)}}
	case domain.ItemTrack:
		return &laternav1.SearchResult{Item: &laternav1.SearchResult_Track{Track: trackMsg(ctx, v)}}
	case domain.ItemBookSeries:
		return &laternav1.SearchResult{Item: &laternav1.SearchResult_BookSeries{BookSeries: bookSeriesMsg(v)}}
	case domain.ItemBook:
		return &laternav1.SearchResult{Item: &laternav1.SearchResult_Book{Book: bookSummaryMsg(ctx, v)}}
	case domain.ItemPhotoAlbum:
		return &laternav1.SearchResult{Item: &laternav1.SearchResult_PhotoAlbum{PhotoAlbum: photoAlbumMsg(v)}}
	case domain.ItemSeason, domain.ItemPhoto:
	}
	return nil
}

// SetPlayed marks an item as played or unplayed.
func (s *CatalogService) SetPlayed(ctx context.Context, req *connect.Request[laternav1.SetPlayedRequest]) (*connect.Response[laternav1.SetPlayedResponse], error) {
	id, err := parseID(req.Msg.GetItemId(), "item_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.SetPlayed(ctx, principal(ctx), id, req.Msg.GetPlayed()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SetPlayedResponse{}), nil
}

// SetFavorite adds or removes a favorite.
func (s *CatalogService) SetFavorite(ctx context.Context, req *connect.Request[laternav1.SetFavoriteRequest]) (*connect.Response[laternav1.SetFavoriteResponse], error) {
	id, err := parseID(req.Msg.GetItemId(), "item_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.SetFavorite(ctx, principal(ctx), id, req.Msg.GetFavorite()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SetFavoriteResponse{}), nil
}

// ListSimilar returns the movies and series close to a movie or a series.
func (s *CatalogService) ListSimilar(ctx context.Context, req *connect.Request[laternav1.ListSimilarRequest]) (*connect.Response[laternav1.ListSimilarResponse], error) {
	id, err := parseID(req.Msg.GetItemId(), "item_id")
	if err != nil {
		return nil, err
	}
	views, err := s.app.Similar(ctx, principal(ctx), id, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListSimilarResponse{}
	for _, v := range views {
		switch v.Item.Kind {
		case domain.ItemMovie:
			resp.Items = append(resp.Items, &laternav1.SimilarItem{Item: &laternav1.SimilarItem_Movie{Movie: movieSummaryMsg(v)}})
		case domain.ItemSeries:
			resp.Items = append(resp.Items, &laternav1.SimilarItem{Item: &laternav1.SimilarItem_Series{Series: seriesSummaryMsg(v)}})
		case domain.ItemSeason, domain.ItemEpisode, domain.ItemArtist, domain.ItemAlbum, domain.ItemTrack, domain.ItemBookSeries,
			domain.ItemBook, domain.ItemPhotoAlbum, domain.ItemPhoto:
		}
	}
	return connect.NewResponse(resp), nil
}
