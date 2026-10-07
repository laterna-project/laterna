-- Subtitle preferences of a profile (internal/playback/subtitles.go): when a playback starts with a
-- subtitle ('' automatic, 'always', 'forced', 'off') and in which language (a BCP 47 tag; empty
-- means the profile's language, then the device's).

-- +goose Up
ALTER TABLE profiles ADD COLUMN subtitle_mode TEXT NOT NULL DEFAULT ''
    CHECK (subtitle_mode IN ('', 'always', 'forced', 'off'));
ALTER TABLE profiles ADD COLUMN subtitle_language TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE profiles DROP COLUMN subtitle_language;
ALTER TABLE profiles DROP COLUMN subtitle_mode;
