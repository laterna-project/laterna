-- OpenID Connect: the identity of a user at the provider (issuer and subject, both stable) linked
-- to a Laterna account.

-- +goose Up
CREATE TABLE oidc_links (
    issuer     TEXT NOT NULL,
    subject    TEXT NOT NULL,
    account_id BLOB NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (issuer, subject)
) STRICT;

CREATE INDEX oidc_links_account ON oidc_links (account_id);

-- +goose Down
DROP TABLE oidc_links;
