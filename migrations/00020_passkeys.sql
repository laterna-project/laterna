-- Passkeys: WebAuthn keys of an account, to sign in without a password.

-- +goose Up
CREATE TABLE passkeys (
    id            BLOB PRIMARY KEY NOT NULL,
    account_id    BLOB NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    -- ID given by the authenticator, as base64url; unique across the server.
    credential_id TEXT NOT NULL UNIQUE,
    -- COSE public key, as base64url.
    public_key    TEXT NOT NULL,
    sign_count    INTEGER NOT NULL CHECK (sign_count >= 0),
    -- Name given at registration ("Alex's iPhone").
    name          TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER
) STRICT;

CREATE INDEX passkeys_account ON passkeys (account_id);

-- +goose Down
DROP TABLE passkeys;
