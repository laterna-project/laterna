-- Audio language a profile prefers, and the audio and subtitle it last chose in each series
-- (docs/design/playback.md): the next episodes start the same way.

-- +goose Up
ALTER TABLE profiles ADD COLUMN audio_language TEXT NOT NULL DEFAULT '';

-- Languages are ISO 639-2/B codes ("jpn", "fre"); an empty audio language means none chosen.
-- subtitle_chosen says a subtitle choice was made: with an empty subtitle language, none.
CREATE TABLE series_choices (
    profile_id        BLOB    NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    series_id         BLOB    NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    audio_language    TEXT    NOT NULL DEFAULT '',
    subtitle_chosen   INTEGER NOT NULL DEFAULT 0 CHECK (subtitle_chosen IN (0, 1)),
    subtitle_language TEXT    NOT NULL DEFAULT '',
    subtitle_forced   INTEGER NOT NULL DEFAULT 0 CHECK (subtitle_forced IN (0, 1)),
    updated_at        INTEGER NOT NULL,
    PRIMARY KEY (profile_id, series_id)
) STRICT;

CREATE INDEX series_choices_series ON series_choices (series_id);

-- +goose Down
DROP TABLE series_choices;

ALTER TABLE profiles DROP COLUMN audio_language;
