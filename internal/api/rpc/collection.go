package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// CollectionService implements laterna.v1.CollectionService.
type CollectionService struct {
	app *app.App
}

func collectionMsg(c domain.CollectionView) *laternav1.Collection {
	return &laternav1.Collection{
		Id: c.ID.String(), Name: c.Name, Overview: c.Overview, Manual: c.Manual, ItemCount: clampInt32(c.ItemCount),
		Images: imagesMsg(c.Images), CreatedAt: timestamppb.New(c.CreatedAt), UpdatedAt: timestamppb.New(c.UpdatedAt),
	}
}

func collectionRefsMsg(cs []domain.Collection) []*laternav1.CollectionRef {
	out := make([]*laternav1.CollectionRef, len(cs))
	for i, c := range cs {
		out[i] = &laternav1.CollectionRef{Id: c.ID.String(), Name: c.Name}
	}
	return out
}

// parseIDs reads a list of IDs.
func parseIDs(raw []string, what string) ([]domain.ID, error) {
	out := make([]domain.ID, 0, len(raw))
	for _, s := range raw {
		id, err := parseID(s, what)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// ListCollections lists the collections.
func (s *CollectionService) ListCollections(ctx context.Context, req *connect.Request[laternav1.ListCollectionsRequest]) (*connect.Response[laternav1.ListCollectionsResponse], error) {
	lib, err := parseOptionalID(req.Msg.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	list, err := s.app.Collections(ctx, principal(ctx), lib)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListCollectionsResponse{}
	for _, c := range list {
		resp.Collections = append(resp.Collections, collectionMsg(c))
	}
	return connect.NewResponse(resp), nil
}

// GetCollection returns a collection and its items.
func (s *CollectionService) GetCollection(ctx context.Context, req *connect.Request[laternav1.GetCollectionRequest]) (*connect.Response[laternav1.GetCollectionResponse], error) {
	id, err := parseID(req.Msg.GetCollectionId(), "collection_id")
	if err != nil {
		return nil, err
	}
	c, items, err := s.app.Collection(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.GetCollectionResponse{Collection: collectionMsg(c)}
	for _, v := range items {
		switch v.Item.Kind {
		case domain.ItemMovie:
			resp.Items = append(resp.Items, &laternav1.CollectionItem{Item: &laternav1.CollectionItem_Movie{Movie: movieSummaryMsg(v)}})
		case domain.ItemSeries:
			resp.Items = append(resp.Items, &laternav1.CollectionItem{Item: &laternav1.CollectionItem_Series{Series: seriesSummaryMsg(v)}})
		case domain.ItemSeason, domain.ItemEpisode, domain.ItemArtist, domain.ItemAlbum, domain.ItemTrack,
			domain.ItemBookSeries, domain.ItemBook, domain.ItemPhotoAlbum, domain.ItemPhoto:
		}
	}
	return connect.NewResponse(resp), nil
}

// CreateCollection creates a collection.
func (s *CollectionService) CreateCollection(ctx context.Context, req *connect.Request[laternav1.CreateCollectionRequest]) (*connect.Response[laternav1.CreateCollectionResponse], error) {
	m := req.Msg
	items, err := parseIDs(m.GetItemIds(), "item_ids")
	if err != nil {
		return nil, err
	}
	c, err := s.app.CreateCollection(ctx, m.GetName(), m.GetOverview(), items)
	if err != nil {
		return nil, err
	}
	v, err := s.view(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreateCollectionResponse{Collection: v}), nil
}

// UpdateCollection changes a collection.
func (s *CollectionService) UpdateCollection(ctx context.Context, req *connect.Request[laternav1.UpdateCollectionRequest]) (*connect.Response[laternav1.UpdateCollectionResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetCollectionId(), "collection_id")
	if err != nil {
		return nil, err
	}
	add, err := parseIDs(m.GetAddItemIds(), "add_item_ids")
	if err != nil {
		return nil, err
	}
	remove, err := parseIDs(m.GetRemoveItemIds(), "remove_item_ids")
	if err != nil {
		return nil, err
	}
	if _, err := s.app.UpdateCollection(ctx, id, app.CollectionChanges{Name: m.Name, Overview: m.Overview, Add: add, Remove: remove}); err != nil {
		return nil, err
	}
	v, err := s.view(ctx, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.UpdateCollectionResponse{Collection: v}), nil
}

// view reads a collection again as the caller sees it, for the response.
func (s *CollectionService) view(ctx context.Context, id domain.ID) (*laternav1.Collection, error) {
	c, _, err := s.app.Collection(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	return collectionMsg(c), nil
}

// DeleteCollection deletes a collection.
func (s *CollectionService) DeleteCollection(ctx context.Context, req *connect.Request[laternav1.DeleteCollectionRequest]) (*connect.Response[laternav1.DeleteCollectionResponse], error) {
	id, err := parseID(req.Msg.GetCollectionId(), "collection_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteCollection(ctx, id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteCollectionResponse{}), nil
}
