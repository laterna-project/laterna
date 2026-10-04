-- Internationalization: the server no longer stores sentences written in a language.
--
-- Activity log: each entry becomes a composed text (key and params, as JSON: domain.Text) that the
-- client translates. Sentences already written stay readable: they become literal texts.
--
-- Profiles: the chosen language (a tag such as "fr" or "pt-BR"; empty means the device's or the
-- server's).
--
-- Names made up by the server for lack of anything better (untitled season, untitled episode,
-- artist and album of an untagged track, untitled volume): stored in English, the way files already
-- spell them, and translated by whoever displays them. An artist whose English name already exists
-- in the library (a compilation tagged "Various Artists") keeps its key: both then have the same
-- name, and the next files join the first one.

-- +goose Up
ALTER TABLE activity ADD COLUMN message TEXT NOT NULL DEFAULT '';

UPDATE activity SET message = json_object('key', 'literal', 'params', json_object('text', summary));

ALTER TABLE activity DROP COLUMN summary;

ALTER TABLE profiles ADD COLUMN language TEXT NOT NULL DEFAULT '';

UPDATE items SET title = 'Season ' || substr(title, 8)
WHERE kind = 'season' AND title GLOB 'Saison [0-9]*' AND substr(title, 8) NOT GLOB '*[^0-9]*';

UPDATE items SET title = 'Specials' WHERE kind = 'season' AND title = 'Épisodes spéciaux';

UPDATE items SET title = 'Episode ' || substr(title, 9)
WHERE kind = 'episode' AND title GLOB 'Épisode [0-9]*' AND substr(title, 9) NOT GLOB '*[^0-9]*';

UPDATE OR IGNORE items SET group_key = 'artist:unknownartist' WHERE kind = 'artist' AND group_key = 'artist:artisteinconnu';

UPDATE OR IGNORE items SET group_key = 'artist:variousartists' WHERE kind = 'artist' AND group_key = 'artist:artistesdivers';

UPDATE items SET title = 'Unknown Artist', sort_title = 'unknown artist' WHERE kind = 'artist' AND title = 'Artiste inconnu';

UPDATE items SET title = 'Various Artists', sort_title = 'various artists' WHERE kind = 'artist' AND title = 'Artistes divers';

UPDATE OR IGNORE items SET group_key = substr(group_key, 1, length(group_key) - 9) || 'unknownalbum'
WHERE kind = 'album' AND group_key LIKE 'album:%:sansalbum';

UPDATE items SET title = 'Unknown Album', sort_title = 'unknown album' WHERE kind = 'album' AND title = 'Sans album';

UPDATE items SET title = replace(title, ' – Tome ', ' – Volume ')
WHERE kind = 'book' AND title LIKE (SELECT s.title FROM items s WHERE s.id = items.parent_id) || ' – Tome %';

UPDATE tracks SET artists = 'Unknown Artist' WHERE artists = 'Artiste inconnu';

UPDATE tracks SET artists = 'Various Artists' WHERE artists = 'Artistes divers';

-- +goose Down
-- Names made up by the server stay in English: only the schema goes back.
ALTER TABLE profiles DROP COLUMN language;

ALTER TABLE activity ADD COLUMN summary TEXT NOT NULL DEFAULT '';

-- An original sentence comes back as it was; of a composed text only the key is left.
UPDATE activity SET summary = COALESCE(json_extract(message, '$.params.text'), json_extract(message, '$.key'), '');

ALTER TABLE activity DROP COLUMN message;
