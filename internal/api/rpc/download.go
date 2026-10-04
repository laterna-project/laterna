package rpc

import (
	"context"
	"strconv"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/playback"
)

// DownloadService implements laterna.v1.DownloadService.
type DownloadService struct {
	app *app.App
}

var downloadQualities = map[laternav1.DownloadQuality]domain.DownloadQuality{
	laternav1.DownloadQuality_DOWNLOAD_QUALITY_UNSPECIFIED: domain.DownloadOriginal,
	laternav1.DownloadQuality_DOWNLOAD_QUALITY_ORIGINAL:    domain.DownloadOriginal,
	laternav1.DownloadQuality_DOWNLOAD_QUALITY_HIGH:        domain.DownloadHigh,
	laternav1.DownloadQuality_DOWNLOAD_QUALITY_MEDIUM:      domain.DownloadMedium,
	laternav1.DownloadQuality_DOWNLOAD_QUALITY_LOW:         domain.DownloadLow,
}

var downloadQualityMsgs = map[domain.DownloadQuality]laternav1.DownloadQuality{
	domain.DownloadOriginal: laternav1.DownloadQuality_DOWNLOAD_QUALITY_ORIGINAL,
	domain.DownloadHigh:     laternav1.DownloadQuality_DOWNLOAD_QUALITY_HIGH,
	domain.DownloadMedium:   laternav1.DownloadQuality_DOWNLOAD_QUALITY_MEDIUM,
	domain.DownloadLow:      laternav1.DownloadQuality_DOWNLOAD_QUALITY_LOW,
}

var downloadStates = map[domain.DownloadState]laternav1.DownloadState{
	domain.DownloadQueued:    laternav1.DownloadState_DOWNLOAD_STATE_QUEUED,
	domain.DownloadPreparing: laternav1.DownloadState_DOWNLOAD_STATE_PREPARING,
	domain.DownloadReady:     laternav1.DownloadState_DOWNLOAD_STATE_READY,
	domain.DownloadFailed:    laternav1.DownloadState_DOWNLOAD_STATE_FAILED,
}

var downloadMethods = map[playback.Method]laternav1.DownloadMethod{
	playback.Direct:    laternav1.DownloadMethod_DOWNLOAD_METHOD_ORIGINAL,
	playback.Remux:     laternav1.DownloadMethod_DOWNLOAD_METHOD_REMUX,
	playback.Transcode: laternav1.DownloadMethod_DOWNLOAD_METHOD_TRANSCODE,
	playback.Convert:   laternav1.DownloadMethod_DOWNLOAD_METHOD_CONVERTED,
}

// DownloadPath is the path of the file of a download (api.downloadFile).
func DownloadPath(id domain.ID) string { return "/downloads/" + id.String() + "/file" }

func downloadMsg(ctx context.Context, v app.DownloadView) *laternav1.Download {
	d, plan := v.Download, v.Plan
	msg := &laternav1.Download{
		Id: d.ID.String(), FileId: d.FileID.String(), Quality: downloadQualityMsgs[d.Quality], State: downloadStates[d.State],
		Method: downloadMethods[plan.Method], Progress: d.Progress, EstimatedSize: d.Estimate, Size: d.Size,
		FileName: v.FileName, VideoTranscoded: plan.Method == playback.Transcode && !plan.CopyVideo,
		AudioTranscoded: (plan.Method == playback.Transcode || plan.Method == playback.Convert) && !plan.CopyAudio,
		Reasons:         renderAll(ctx, plan.Reasons), ReasonTexts: textsMsg(ctx, plan.Reasons), Error: d.Error, CreatedAt: timestamppb.New(d.CreatedAt), Duration: durationMsg(v.Item.Item.Runtime),
		Fonts: fonts(v.Fonts),
	}
	switch v.Item.Item.Kind {
	case domain.ItemMovie:
		msg.Item = &laternav1.Download_Movie{Movie: movieSummaryMsg(v.Item)}
	case domain.ItemEpisode:
		msg.Item = &laternav1.Download_Episode{Episode: episodeMsg(ctx, v.Item)}
	case domain.ItemTrack:
		msg.Item = &laternav1.Download_Track{Track: trackMsg(ctx, v.Item)}
	case domain.ItemSeries, domain.ItemSeason, domain.ItemArtist, domain.ItemAlbum, domain.ItemBookSeries, domain.ItemBook,
		domain.ItemPhotoAlbum, domain.ItemPhoto:
	}
	if d.State == domain.DownloadReady {
		msg.Url = DownloadPath(d.ID)
	}
	if d.ReadyAt != nil {
		msg.ReadyAt = timestamppb.New(*d.ReadyAt)
	}
	if v.ExpiresAt != nil {
		msg.ExpiresAt = timestamppb.New(*v.ExpiresAt)
	}
	if plan.Audio >= 0 {
		a := clampInt32(plan.Audio)
		msg.AudioStreamIndex = &a
	}
	for _, st := range v.Subtitles {
		track := &laternav1.SubtitleTrack{
			Index: clampInt32(st.Position), Language: st.Language, Title: st.Title, Codec: st.Codec,
			Default: st.Default, Forced: st.Forced, HearingImpaired: st.HearingImpaired,
			External: st.External(), Image: st.Image(),
		}
		for _, format := range st.Formats {
			track.Files = append(track.Files, &laternav1.SubtitleFile{
				Format: format, Url: "/downloads/" + d.ID.String() + "/subtitles/" + strconv.Itoa(st.Position) + "." + format,
			})
		}
		msg.Subtitles = append(msg.Subtitles, track)
	}
	return msg
}

func downloadsMsg(ctx context.Context, views []app.DownloadView) []*laternav1.Download {
	out := make([]*laternav1.Download, len(views))
	for i, v := range views {
		out[i] = downloadMsg(ctx, v)
	}
	return out
}

// CreateDownloads asks for downloads.
func (s *DownloadService) CreateDownloads(ctx context.Context, req *connect.Request[laternav1.CreateDownloadsRequest]) (*connect.Response[laternav1.CreateDownloadsResponse], error) {
	m := req.Msg
	ids, err := parseIDs(m.GetItemIds(), "item_ids")
	if err != nil {
		return nil, err
	}
	file, err := parseOptionalID(m.GetFileId(), "file_id")
	if err != nil {
		return nil, err
	}
	quality, ok := downloadQualities[m.GetQuality()]
	if !ok {
		return nil, domain.Invalid("download.unknown_quality")
	}
	audio := -1
	if m.AudioStreamIndex != nil {
		audio = int(m.GetAudioStreamIndex())
	}
	views, err := s.app.CreateDownloads(ctx, principal(ctx), app.DownloadRequest{
		ItemIDs: ids, FileID: file, Quality: quality, Audio: audio, Device: deviceProfileFromMsg(m.GetDevice()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreateDownloadsResponse{Downloads: downloadsMsg(ctx, views)}), nil
}

// ListDownloads lists the downloads of the device.
func (s *DownloadService) ListDownloads(ctx context.Context, _ *connect.Request[laternav1.ListDownloadsRequest]) (*connect.Response[laternav1.ListDownloadsResponse], error) {
	views, err := s.app.ListDownloads(ctx, principal(ctx))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ListDownloadsResponse{Downloads: downloadsMsg(ctx, views)}), nil
}

// GetDownload returns a download of the device.
func (s *DownloadService) GetDownload(ctx context.Context, req *connect.Request[laternav1.GetDownloadRequest]) (*connect.Response[laternav1.GetDownloadResponse], error) {
	id, err := parseID(req.Msg.GetDownloadId(), "download_id")
	if err != nil {
		return nil, err
	}
	v, err := s.app.GetDownload(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.GetDownloadResponse{Download: downloadMsg(ctx, v)}), nil
}

// DeleteDownloads removes downloads.
func (s *DownloadService) DeleteDownloads(ctx context.Context, req *connect.Request[laternav1.DeleteDownloadsRequest]) (*connect.Response[laternav1.DeleteDownloadsResponse], error) {
	ids, err := parseIDs(req.Msg.GetDownloadIds(), "download_ids")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteDownloads(ctx, principal(ctx), ids); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteDownloadsResponse{}), nil
}

// SyncOfflinePlayback reports what was played offline.
func (s *DownloadService) SyncOfflinePlayback(ctx context.Context, req *connect.Request[laternav1.SyncOfflinePlaybackRequest]) (*connect.Response[laternav1.SyncOfflinePlaybackResponse], error) {
	plays := make([]app.OfflinePlay, 0, len(req.Msg.GetPlays()))
	for _, pl := range req.Msg.GetPlays() {
		id, err := parseID(pl.GetItemId(), "item_id")
		if err != nil {
			return nil, err
		}
		play := app.OfflinePlay{ItemID: id, Position: pl.GetPosition().AsDuration(), Finished: pl.GetFinished()}
		if pl.GetPlayedAt() != nil {
			play.At = pl.GetPlayedAt().AsTime()
		}
		plays = append(plays, play)
	}
	if err := s.app.SyncOfflinePlayback(ctx, principal(ctx), plays); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SyncOfflinePlaybackResponse{}), nil
}
