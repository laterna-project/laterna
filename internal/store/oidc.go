package store

import (
	"context"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// OIDC: provider identities linked to accounts.

// OIDCAccount returns the account linked to a provider identity.
func (q Q) OIDCAccount(ctx context.Context, issuer, subject string) (domain.ID, error) {
	return q.q.GetOidcAccount(ctx, sqlc.GetOidcAccountParams{Issuer: issuer, Subject: subject})
}

// LinkOIDC links a provider identity to an account; ErrDuplicate if it is already linked.
func (q Q) LinkOIDC(ctx context.Context, issuer, subject string, accountID domain.ID, now time.Time) error {
	return translate(q.q.InsertOidcLink(ctx, sqlc.InsertOidcLinkParams{
		Issuer: issuer, Subject: subject, AccountID: accountID, CreatedAt: toMillis(now),
	}))
}
