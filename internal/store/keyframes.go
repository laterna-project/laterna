package store

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Keyframes reads the keyframe index of a file, if it was computed for this fingerprint (ok is
// false otherwise).
func (q Q) Keyframes(ctx context.Context, fileID domain.ID, fingerprint string) (times []time.Duration, ok bool, err error) {
	enc, err := q.q.GetKeyframes(ctx, sqlc.GetKeyframesParams{FileID: fileID, Fingerprint: fingerprint})
	if IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	times, err = decodeTimes(enc)
	return times, err == nil, err
}

// SetKeyframes stores the keyframe index of a file.
func (q Q) SetKeyframes(ctx context.Context, fileID domain.ID, fingerprint string, times []time.Duration, now time.Time) error {
	return q.q.UpsertKeyframes(ctx, sqlc.UpsertKeyframesParams{
		FileID: fileID, Fingerprint: fingerprint, Times: encodeTimes(times), CreatedAt: toMillis(now),
	})
}

// encodeTimes writes increasing times as deltas in microseconds, varint encoded, in base64.
func encodeTimes(times []time.Duration) string {
	buf := make([]byte, 0, len(times)*3)
	var prev int64
	for _, t := range times {
		us := t.Microseconds()
		buf = binary.AppendUvarint(buf, uint64(max(us-prev, 0)))
		prev = us
	}
	return base64.RawStdEncoding.EncodeToString(buf)
}

func decodeTimes(enc string) ([]time.Duration, error) {
	buf, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	var out []time.Duration
	var us int64
	for len(buf) > 0 {
		d, n := binary.Uvarint(buf)
		if n <= 0 {
			return nil, errors.New("store: unreadable keyframe index")
		}
		us += int64(d) //nolint:gosec // deltas are bounded by the duration of a media file
		out = append(out, time.Duration(us)*time.Microsecond)
		buf = buf[n:]
	}
	return out, nil
}
