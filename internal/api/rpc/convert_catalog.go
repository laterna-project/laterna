package rpc

import (
	"context"
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// Catalog: domain types to contract messages.

// imageURL is the URL of an image (byte route). The hash is part of it, so it changes when the
// image changes.
func imageURL(img domain.Image) string { return "/images/" + img.ID.String() + "/" + img.Hash }

var imageKinds = map[domain.ImageKind]laternav1.ImageKind{
	domain.ImagePoster:   laternav1.ImageKind_IMAGE_KIND_POSTER,
	domain.ImageBackdrop: laternav1.ImageKind_IMAGE_KIND_BACKDROP,
	domain.ImageLogo:     laternav1.ImageKind_IMAGE_KIND_LOGO,
	domain.ImageThumb:    laternav1.ImageKind_IMAGE_KIND_THUMB,
	domain.ImageBanner:   laternav1.ImageKind_IMAGE_KIND_BANNER,
	domain.ImagePhoto:    laternav1.ImageKind_IMAGE_KIND_PHOTO,
}

func imagesMsg(imgs []domain.Image) []*laternav1.Image {
	out := make([]*laternav1.Image, 0, len(imgs))
	for _, img := range imgs {
		out = append(out, &laternav1.Image{
			Kind: imageKinds[img.Kind], Url: imageURL(img),
			Width: clampInt32(img.Width), Height: clampInt32(img.Height), Blurhash: img.BlurHash,
		})
	}
	return out
}

func userDataMsg(u domain.UserData) *laternav1.UserData {
	msg := &laternav1.UserData{Played: u.Played, PlayCount: clampInt32(u.PlayCount), Favorite: u.Favorite}
	if u.Position > 0 {
		msg.Position = durationpb.New(u.Position)
	}
	if u.LastPlayedAt != nil {
		msg.LastPlayedAt = timestamppb.New(*u.LastPlayedAt)
	}
	return msg
}

// durationMsg returns nil for an unknown duration (0).
func durationMsg(d time.Duration) *durationpb.Duration {
	if d <= 0 {
		return nil
	}
	return durationpb.New(d)
}

func movieSummaryMsg(v domain.ItemView) *laternav1.MovieSummary {
	it := v.Item
	return &laternav1.MovieSummary{
		Id: it.ID.String(), LibraryId: it.LibraryID.String(), Title: it.Title, Year: clampInt32(it.Year),
		Runtime: durationMsg(it.Runtime), OfficialRating: it.OfficialRating, CommunityRating: it.CommunityRating,
		Images: imagesMsg(v.Images), UserData: userDataMsg(v.UserData), AddedAt: timestamppb.New(it.AddedAt),
	}
}

func seriesSummaryMsg(v domain.ItemView) *laternav1.SeriesSummary {
	it := v.Item
	return &laternav1.SeriesSummary{
		Id: it.ID.String(), LibraryId: it.LibraryID.String(), Title: it.Title, Year: clampInt32(it.Year),
		OfficialRating: it.OfficialRating, CommunityRating: it.CommunityRating, Images: imagesMsg(v.Images),
		EpisodeCount: clampInt32(v.EpisodeCount), UnplayedCount: clampInt32(v.UnplayedCount),
		UserData: userDataMsg(v.UserData), AddedAt: timestamppb.New(it.AddedAt),
	}
}

func seasonMsg(ctx context.Context, v domain.ItemView) *laternav1.Season {
	msg := &laternav1.Season{
		Id: v.Item.ID.String(), Title: v.Item.Title, Overview: v.Item.Overview, Images: imagesMsg(v.Images),
		EpisodeCount: clampInt32(v.EpisodeCount), UnplayedCount: clampInt32(v.UnplayedCount), UserData: userDataMsg(v.UserData),
	}
	if s := v.Season; s != nil {
		msg.SeriesId, msg.Number = s.SeriesID.String(), clampInt32(s.Number)
		name, ok := domain.SeasonName(v.Item.Title, s.Number)
		msg.Title, msg.TitleText = given(ctx, v.Item.Title, name, ok)
	}
	return msg
}

func episodeMsg(ctx context.Context, v domain.ItemView) *laternav1.Episode {
	it := v.Item
	msg := &laternav1.Episode{
		Id: it.ID.String(), SeriesTitle: v.SeriesTitle, Title: it.Title, Overview: it.Overview,
		PremiereDate: it.PremiereDate, Runtime: durationMsg(it.Runtime), CommunityRating: it.CommunityRating,
		Images: imagesMsg(v.Images), UserData: userDataMsg(v.UserData), AddedAt: timestamppb.New(it.AddedAt),
	}
	if e := v.Episode; e != nil {
		msg.SeriesId, msg.SeasonId = e.SeriesID.String(), e.SeasonID.String()
		msg.SeasonNumber, msg.Number, msg.NumberEnd = clampInt32(e.SeasonNumber), clampInt32(e.Number), clampInt32(e.NumberEnd)
		name, ok := domain.EpisodeName(it.Title, e.Number)
		msg.Title, msg.TitleText = given(ctx, it.Title, name, ok)
	}
	return msg
}

func artistSummaryMsg(ctx context.Context, v domain.ItemView) *laternav1.ArtistSummary {
	it := v.Item
	msg := &laternav1.ArtistSummary{
		Id: it.ID.String(), LibraryId: it.LibraryID.String(), Images: imagesMsg(v.Images),
		AlbumCount: clampInt32(v.AlbumCount), TrackCount: clampInt32(v.TrackCount), UserData: userDataMsg(v.UserData),
		AddedAt: timestamppb.New(it.AddedAt),
	}
	name, ok := domain.ArtistName(it.Title)
	msg.Name, msg.NameText = given(ctx, it.Title, name, ok)
	return msg
}

func albumSummaryMsg(ctx context.Context, v domain.ItemView) *laternav1.AlbumSummary {
	it := v.Item
	msg := &laternav1.AlbumSummary{
		Id: it.ID.String(), LibraryId: it.LibraryID.String(), ArtistName: artistName(ctx, v.ArtistName),
		Year: clampInt32(it.Year), Images: imagesMsg(v.Images), TrackCount: clampInt32(v.TrackCount),
		Runtime: durationMsg(it.Runtime), UserData: userDataMsg(v.UserData), AddedAt: timestamppb.New(it.AddedAt),
	}
	name, ok := domain.AlbumName(it.Title)
	msg.Title, msg.TitleText = given(ctx, it.Title, name, ok)
	if it.ParentID != nil {
		msg.ArtistId = it.ParentID.String()
	}
	return msg
}

func trackMsg(ctx context.Context, v domain.ItemView) *laternav1.Track {
	it := v.Item
	msg := &laternav1.Track{
		Id: it.ID.String(), AlbumTitle: albumTitle(ctx, v.AlbumTitle), ArtistName: artistName(ctx, v.ArtistName), Title: it.Title,
		Runtime: durationMsg(it.Runtime), Images: imagesMsg(v.Images), UserData: userDataMsg(v.UserData),
		AddedAt: timestamppb.New(it.AddedAt),
	}
	if t := v.Track; t != nil {
		msg.AlbumId, msg.ArtistId, msg.Artists = t.AlbumID.String(), t.ArtistID.String(), artistName(ctx, t.Artists)
		msg.Disc, msg.Number = clampInt32(t.Disc), clampInt32(t.Number)
		if t.TrackGain != nil || t.TrackPeak != nil || t.AlbumGain != nil || t.AlbumPeak != nil {
			msg.ReplayGain = &laternav1.ReplayGain{TrackGain: t.TrackGain, TrackPeak: t.TrackPeak, AlbumGain: t.AlbumGain, AlbumPeak: t.AlbumPeak}
		}
	}
	return msg
}

var creditRoles = map[domain.PersonRole]laternav1.CreditRole{
	domain.RoleActor:       laternav1.CreditRole_CREDIT_ROLE_ACTOR,
	domain.RoleDirector:    laternav1.CreditRole_CREDIT_ROLE_DIRECTOR,
	domain.RoleWriter:      laternav1.CreditRole_CREDIT_ROLE_WRITER,
	domain.RoleIllustrator: laternav1.CreditRole_CREDIT_ROLE_ILLUSTRATOR,
}

func creditsMsg(credits []domain.Credit) []*laternav1.Credit {
	out := make([]*laternav1.Credit, len(credits))
	for i, c := range credits {
		out[i] = &laternav1.Credit{PersonId: c.PersonID.String(), Name: c.Name, Role: creditRoles[c.Role], Character: c.Character}
		if c.Image != nil {
			out[i].Image = imagesMsg([]domain.Image{*c.Image})[0]
		}
	}
	return out
}

var streamKinds = map[domain.StreamKind]laternav1.StreamKind{
	domain.StreamVideo:    laternav1.StreamKind_STREAM_KIND_VIDEO,
	domain.StreamAudio:    laternav1.StreamKind_STREAM_KIND_AUDIO,
	domain.StreamSubtitle: laternav1.StreamKind_STREAM_KIND_SUBTITLE,
}

var dynamicRanges = map[domain.DynamicRange]laternav1.DynamicRange{
	domain.SDR:         laternav1.DynamicRange_DYNAMIC_RANGE_SDR,
	domain.HDR10:       laternav1.DynamicRange_DYNAMIC_RANGE_HDR10,
	domain.HLG:         laternav1.DynamicRange_DYNAMIC_RANGE_HLG,
	domain.DolbyVision: laternav1.DynamicRange_DYNAMIC_RANGE_DOLBY_VISION,
}

func filesMsg(files []app.FileVersion) []*laternav1.MediaFile {
	out := make([]*laternav1.MediaFile, len(files))
	for i, fv := range files {
		f := fv.File
		msg := &laternav1.MediaFile{
			Id: f.ID.String(), Version: fv.Version, Part: clampInt32(fv.Part), Available: f.MissingSince == nil,
			Container: f.Info.Container, Duration: durationMsg(f.Info.Duration), Size: f.Size, Bitrate: f.Info.Bitrate,
		}
		for _, s := range f.Info.Streams {
			kind, ok := streamKinds[s.Kind]
			if !ok {
				continue // attachments, data: of no use to a client
			}
			msg.Streams = append(msg.Streams, &laternav1.MediaStream{
				Index: clampInt32(s.Index), Kind: kind, Codec: s.Codec, Profile: s.Profile, Language: s.Language,
				Title: s.Title, Default: s.Default, Forced: s.Forced, HearingImpaired: s.HearingImpaired,
				Width: clampInt32(s.Width), Height: clampInt32(s.Height), BitDepth: clampInt32(s.BitDepth),
				FrameRate: s.FrameRate, DynamicRange: dynamicRanges[s.DynamicRange], Channels: clampInt32(s.Channels),
				ChannelLayout: s.ChannelLayout, SampleRate: clampInt32(s.SampleRate), Bitrate: s.Bitrate,
			})
		}
		for _, c := range f.Info.Chapters {
			msg.Chapters = append(msg.Chapters, &laternav1.Chapter{Start: durationpb.New(c.Start), End: durationpb.New(c.End), Title: c.Title})
		}
		msg.Segments = mediaSegments(f.Segments)
		if t := fv.Trickplay; t != nil {
			tp := &laternav1.Trickplay{
				Interval: durationpb.New(t.Interval), Width: clampInt32(t.Width), Height: clampInt32(t.Height),
				Columns: clampInt32(t.Columns), Rows: clampInt32(t.Rows), Count: clampInt32(t.Count),
			}
			for n := range t.Sheets {
				tp.Sheets = append(tp.Sheets, fmt.Sprintf("/trickplay/%s/%s/%03d.jpg", f.ID, t.Key, n))
			}
			msg.Trickplay = tp
		}
		if fv.Book != nil {
			msg.Book = bookFileMsg(*fv.Book)
		}
		out[i] = msg
	}
	return out
}

func itemSortFromMsg(s laternav1.ItemSort) domain.ItemSort {
	switch s {
	case laternav1.ItemSort_ITEM_SORT_ADDED:
		return domain.SortAdded
	case laternav1.ItemSort_ITEM_SORT_RELEASED:
		return domain.SortReleased
	case laternav1.ItemSort_ITEM_SORT_RATING:
		return domain.SortRating
	case laternav1.ItemSort_ITEM_SORT_UNSPECIFIED, laternav1.ItemSort_ITEM_SORT_TITLE:
		return domain.SortTitle
	}
	return domain.ItemSort("unknown") // value from a newer client: rejected by app
}

// clampInt32 clamps an integer to the int32 range of the contract.
func clampInt32(n int) int32 {
	return int32(max(math.MinInt32, min(n, math.MaxInt32)))
}

// parseOptionalID reads an optional ID: empty gives nil.
func parseOptionalID(s, what string) (*domain.ID, error) {
	if s == "" {
		return nil, nil //nolint:nilnil // absence on purpose
	}
	id, err := parseID(s, what)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

var segmentKinds = map[domain.SegmentKind]laternav1.MediaSegmentKind{
	domain.SegmentIntro:   laternav1.MediaSegmentKind_MEDIA_SEGMENT_KIND_INTRO,
	domain.SegmentCredits: laternav1.MediaSegmentKind_MEDIA_SEGMENT_KIND_CREDITS,
	domain.SegmentRecap:   laternav1.MediaSegmentKind_MEDIA_SEGMENT_KIND_RECAP,
	domain.SegmentPreview: laternav1.MediaSegmentKind_MEDIA_SEGMENT_KIND_PREVIEW,
}

// mediaSegments converts the intros, credits and other skippable segments of a file.
func mediaSegments(segs []domain.Segment) []*laternav1.MediaSegment {
	out := make([]*laternav1.MediaSegment, 0, len(segs))
	for _, s := range segs {
		if kind, ok := segmentKinds[s.Kind]; ok {
			out = append(out, &laternav1.MediaSegment{Kind: kind, Start: durationpb.New(s.Start), End: durationpb.New(s.End)})
		}
	}
	return out
}
