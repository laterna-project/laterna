package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// SubtitleService implements laterna.v1.SubtitleService.
type SubtitleService struct {
	app *app.App
}

var subtitleSearchStates = map[domain.SubtitleSearchState]laternav1.SubtitleSearchState{
	domain.SubtitleSearching:    laternav1.SubtitleSearchState_SUBTITLE_SEARCH_STATE_SEARCHING,
	domain.SubtitleFound:        laternav1.SubtitleSearchState_SUBTITLE_SEARCH_STATE_FOUND,
	domain.SubtitleNotFound:     laternav1.SubtitleSearchState_SUBTITLE_SEARCH_STATE_NOT_FOUND,
	domain.SubtitleSearchFailed: laternav1.SubtitleSearchState_SUBTITLE_SEARCH_STATE_FAILED,
}

func subtitleSearchMsg(ctx context.Context, s domain.SubtitleSearch) *laternav1.SubtitleSearch {
	msg := &laternav1.SubtitleSearch{
		FileId: s.FileID.String(), Language: s.Language, HearingImpaired: s.HearingImpaired, Forced: s.Forced,
		State: subtitleSearchStates[s.State], StartedAt: timestamppb.New(s.StartedAt),
	}
	if s.Error != nil {
		msg.Error, msg.ErrorText = render(ctx, *s.Error), textMsg(ctx, *s.Error)
	}
	return msg
}

// GetSubtitleSearch says whether a subtitle can be looked for a file, and where its searches stand.
func (s *SubtitleService) GetSubtitleSearch(ctx context.Context, req *connect.Request[laternav1.GetSubtitleSearchRequest]) (*connect.Response[laternav1.GetSubtitleSearchResponse], error) {
	id, err := parseID(req.Msg.GetFileId(), "file_id")
	if err != nil {
		return nil, err
	}
	info, err := s.app.SubtitleSearch(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	out := &laternav1.GetSubtitleSearchResponse{Available: info.Available}
	for _, l := range info.Languages {
		out.Languages = append(out.Languages, &laternav1.SubtitleLanguage{Code: l.Code, Name: l.Name})
	}
	for _, search := range info.Searches {
		out.Searches = append(out.Searches, subtitleSearchMsg(ctx, search))
	}
	return connect.NewResponse(out), nil
}

// SearchSubtitle asks for a subtitle of a file.
func (s *SubtitleService) SearchSubtitle(ctx context.Context, req *connect.Request[laternav1.SearchSubtitleRequest]) (*connect.Response[laternav1.SearchSubtitleResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetFileId(), "file_id")
	if err != nil {
		return nil, err
	}
	search, err := s.app.SearchSubtitle(ctx, principal(ctx), id, domain.SubtitleWanted{
		Language: m.GetLanguage(), HearingImpaired: m.GetHearingImpaired(), Forced: m.GetForced(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SearchSubtitleResponse{Search: subtitleSearchMsg(ctx, search)}), nil
}
