package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

func TestKeyframes(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	lib := newLibrary("Movies", domain.LibraryMovies, "/m")
	file := domain.MediaFile{ID: domain.NewID(), LibraryID: lib.ID, Path: "/m/a.mkv", Size: 1, ModTime: t0, Fingerprint: "fingerprint"}
	mustWrite(t, st, func(q Q) error {
		if err := q.CreateLibrary(ctx, lib); err != nil {
			return err
		}
		return q.CreateFile(ctx, file, t0)
	})
	times := []time.Duration{0, 83 * time.Millisecond, 6256 * time.Millisecond, 1541930 * time.Millisecond}
	mustWrite(t, st, func(q Q) error { return q.SetKeyframes(ctx, file.ID, file.Fingerprint, times, t0) })

	got, ok, err := st.Read().Keyframes(ctx, file.ID, file.Fingerprint)
	if err != nil || !ok || !slices.Equal(got, times) {
		t.Fatalf("read back: %v %v %v", got, ok, err)
	}
	// Changed file (another fingerprint): the index no longer applies.
	if _, ok, err := st.Read().Keyframes(ctx, file.ID, "other"); ok || err != nil {
		t.Errorf("different fingerprint: %v %v", ok, err)
	}
	if enc := encodeTimes(times); len(enc) > 24 { // 4 times, one of them 25 min after the previous
		t.Errorf("encoding is not compact: %q", enc)
	}
}
