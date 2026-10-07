package rpc

import (
	"context"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/playback"
)

// PlaybackService implements laterna.v1.PlaybackService.
type PlaybackService struct {
	app *app.App
}

var playbackMethods = map[playback.Method]laternav1.PlaybackMethod{
	playback.Direct:    laternav1.PlaybackMethod_PLAYBACK_METHOD_DIRECT,
	playback.Remux:     laternav1.PlaybackMethod_PLAYBACK_METHOD_HLS_REMUX,
	playback.Transcode: laternav1.PlaybackMethod_PLAYBACK_METHOD_HLS_TRANSCODE,
	playback.Convert:   laternav1.PlaybackMethod_PLAYBACK_METHOD_CONVERTED,
}

// deviceProfileFromMsg converts what the device says it can play.
func deviceProfileFromMsg(dev *laternav1.DeviceProfile) playback.DeviceProfile {
	profile := playback.DeviceProfile{
		Containers: dev.GetContainers(), AudioCodecs: dev.GetAudioCodecs(), HLS: dev.GetHls(),
		SubtitleFormats: dev.GetSubtitleFormats(),
	}
	for _, v := range dev.GetVideo() {
		profile.Video = append(profile.Video, playback.VideoSupport{Codec: v.GetCodec(), MaxBitDepth: int(v.GetMaxBitDepth()), HDR: v.GetHdr()})
	}
	return profile
}

// StreamPath is the path of the stream of a playback (byte route, api.playback): the file, as is or
// converted, otherwise the HLS playlist.
func StreamPath(info app.PlayInfo) string {
	if info.Method == playback.Direct || info.Method == playback.Convert {
		return sessionPath(info) + "direct"
	}
	return sessionPath(info) + "main.m3u8"
}

func sessionPath(info app.PlayInfo) string {
	return "/playback/" + info.SessionID.String() + "/" + info.Token + "/"
}

// SubtitlePath is the path of a subtitle of a playback in a given format (api.subtitle).
func SubtitlePath(info app.PlayInfo, position int, format string) string {
	return sessionPath(info) + "subtitles/" + strconv.Itoa(position) + "." + format
}

// FontPath is the path of a font (api.font).
func FontPath(f domain.Font) string { return "/fonts/" + f.SHA256 + f.Ext }

// StartPlayback opens a playback.
func (s *PlaybackService) StartPlayback(ctx context.Context, req *connect.Request[laternav1.StartPlaybackRequest]) (*connect.Response[laternav1.StartPlaybackResponse], error) {
	m := req.Msg
	item, err := parseID(m.GetItemId(), "item_id")
	if err != nil {
		return nil, err
	}
	file, err := parseOptionalID(m.GetFileId(), "file_id")
	if err != nil {
		return nil, err
	}
	audio := -1
	if m.AudioStreamIndex != nil {
		audio = int(m.GetAudioStreamIndex())
	}
	profile := deviceProfileFromMsg(m.GetDevice())
	var subtitle *int
	if m.SubtitleIndex != nil {
		n := int(m.GetSubtitleIndex())
		subtitle = &n
	}
	info, err := s.app.StartPlayback(ctx, principal(ctx), app.PlayRequest{
		ItemID: item, FileID: file, Audio: audio, Device: profile, Subtitle: subtitle,
		ProfileSubtitle: m.GetProfileSubtitle(), Language: firstLanguage(req.Header().Get("Accept-Language")),
	})
	if err != nil {
		return nil, err
	}
	resp := &laternav1.StartPlaybackResponse{
		SessionId: info.SessionID.String(), Method: playbackMethods[info.Method], Url: StreamPath(info),
		FileId: info.File.ID.String(), Duration: durationMsg(info.Duration),
		Reasons: renderAll(ctx, info.Reasons), ReasonTexts: textsMsg(ctx, info.Reasons), VideoTranscoded: !info.CopyVideo, AudioTranscoded: !info.CopyAudio, VideoEncoder: info.Encoder,
		SubtitlesReady: info.SubtitlesReady, Subtitles: subtitleTracks(info), Fonts: fonts(info.Fonts),
		ToneMapping: info.ToneMap, Gpu: info.GPU, Segments: mediaSegments(info.File.Segments),
	}
	if info.Subtitle != nil {
		n := clampInt32(*info.Subtitle)
		resp.SubtitleIndex = &n
	}
	if info.Burned != nil {
		b := clampInt32(*info.Burned)
		resp.BurnedSubtitleIndex = &b
	}
	if info.Audio >= 0 {
		a := clampInt32(info.Audio)
		resp.AudioStreamIndex = &a
	}
	if info.Resume > 0 {
		resp.ResumePosition = durationpb.New(info.Resume)
	}
	return connect.NewResponse(resp), nil
}

// GetSubtitles reads the subtitles of a playback again.
func (s *PlaybackService) GetSubtitles(ctx context.Context, req *connect.Request[laternav1.GetSubtitlesRequest]) (*connect.Response[laternav1.GetSubtitlesResponse], error) {
	id, err := parseID(req.Msg.GetSessionId(), "session_id")
	if err != nil {
		return nil, err
	}
	info, err := s.app.PlaybackSubtitles(ctx, principal(ctx), id, req.Msg.GetWait())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.GetSubtitlesResponse{
		Subtitles: subtitleTracks(info), Fonts: fonts(info.Fonts), SubtitlesReady: info.SubtitlesReady,
	}), nil
}

func subtitleTracks(info app.PlayInfo) []*laternav1.SubtitleTrack {
	out := make([]*laternav1.SubtitleTrack, 0, len(info.Subtitles))
	for _, st := range info.Subtitles {
		track := &laternav1.SubtitleTrack{
			Index: clampInt32(st.Position), Language: st.Language, Title: st.Title, Codec: st.Codec,
			Default: st.Default, Forced: st.Forced, HearingImpaired: st.HearingImpaired,
			External: st.External(), Image: st.Image(),
		}
		for _, format := range st.Formats {
			track.Files = append(track.Files, &laternav1.SubtitleFile{Format: format, Url: SubtitlePath(info, st.Position, format)})
		}
		out = append(out, track)
	}
	return out
}

func fonts(list []domain.Font) []*laternav1.Font {
	out := make([]*laternav1.Font, 0, len(list))
	for _, f := range list {
		out = append(out, &laternav1.Font{Names: f.Names, Url: FontPath(f), Size: f.Size})
	}
	return out
}

// ReportProgress records the playback position.
func (s *PlaybackService) ReportProgress(ctx context.Context, req *connect.Request[laternav1.ReportProgressRequest]) (*connect.Response[laternav1.ReportProgressResponse], error) {
	id, err := parseID(req.Msg.GetSessionId(), "session_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.ReportProgress(principal(ctx), id, req.Msg.GetPosition().AsDuration()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ReportProgressResponse{}), nil
}

// StopPlayback ends a playback.
func (s *PlaybackService) StopPlayback(ctx context.Context, req *connect.Request[laternav1.StopPlaybackRequest]) (*connect.Response[laternav1.StopPlaybackResponse], error) {
	id, err := parseID(req.Msg.GetSessionId(), "session_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.StopPlayback(ctx, principal(ctx), id, req.Msg.GetPosition().AsDuration()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.StopPlaybackResponse{}), nil
}

// firstLanguage is the first language of an Accept-Language header ("fr-FR,fr;q=0.9" gives
// "fr-FR"), the one the device prefers; "" without one.
func firstLanguage(header string) string {
	first, _, _ := strings.Cut(header, ",")
	tag, _, _ := strings.Cut(first, ";")
	if tag = strings.TrimSpace(tag); tag == "*" {
		return ""
	}
	return tag
}
