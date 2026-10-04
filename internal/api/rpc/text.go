package rpc

import (
	"context"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/i18n"
)

// Texts composed by the server: each one is written in the language of the request and also given
// for translation (key and params) to clients that prefer their own wording.

// render writes a text in the language of the request; empty for an empty text.
func render(ctx context.Context, t domain.Text) string {
	if t.IsZero() {
		return ""
	}
	return i18n.Render(i18n.FromContext(ctx), t)
}

// textMsg gives a text for translation; nil for an empty text.
func textMsg(ctx context.Context, t domain.Text) *laternav1.Text {
	if t.IsZero() {
		return nil
	}
	return textIn(i18n.FromContext(ctx), t)
}

func textIn(lang i18n.Lang, t domain.Text) *laternav1.Text {
	msg := &laternav1.Text{Key: t.Key, Params: t.Params, Text: i18n.Render(lang, t)}
	for _, sub := range t.List {
		msg.List = append(msg.List, textIn(lang, sub))
	}
	return msg
}

// renderAll and textsMsg do the same for a list (reasons for a transcode).
func renderAll(ctx context.Context, texts []domain.Text) []string {
	if len(texts) == 0 {
		return nil
	}
	lang := i18n.FromContext(ctx)
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = i18n.Render(lang, t)
	}
	return out
}

func textsMsg(ctx context.Context, texts []domain.Text) []*laternav1.Text {
	if len(texts) == 0 {
		return nil
	}
	lang := i18n.FromContext(ctx)
	out := make([]*laternav1.Text, len(texts))
	for i, t := range texts {
		out[i] = textIn(lang, t)
	}
	return out
}

// given writes a name the server may have made up itself (untitled season, unknown artist):
// translated if it is one, as is otherwise. The returned text is nil for a real name.
func given(ctx context.Context, name string, t domain.Text, ok bool) (string, *laternav1.Text) {
	if !ok {
		return name, nil
	}
	msg := textMsg(ctx, t)
	return msg.GetText(), msg
}

// artistName writes the name of an artist where it is only a mention (album, track).
func artistName(ctx context.Context, name string) string {
	t, ok := domain.ArtistName(name)
	out, _ := given(ctx, name, t, ok)
	return out
}

// albumTitle writes the title of an album where it is only a mention (track).
func albumTitle(ctx context.Context, title string) string {
	t, ok := domain.AlbumName(title)
	out, _ := given(ctx, title, t, ok)
	return out
}
