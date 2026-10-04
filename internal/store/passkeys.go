package store

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Passkeys: IDs and keys are stored as base64url (only the server's own IDs are BLOBs).

var b64url = base64.RawURLEncoding

// AddPasskey stores a passkey; ErrDuplicate if its credential ID is already registered.
func (q Q) AddPasskey(ctx context.Context, p domain.Passkey) error {
	return translate(q.q.InsertPasskey(ctx, sqlc.InsertPasskeyParams{
		ID: p.ID, AccountID: p.AccountID, CredentialID: b64url.EncodeToString(p.CredentialID),
		PublicKey: b64url.EncodeToString(p.PublicKey), SignCount: int64(p.SignCount), Name: p.Name, CreatedAt: toMillis(p.CreatedAt),
	}))
}

// PasskeyByCredential finds a passkey by the authenticator's credential ID.
func (q Q) PasskeyByCredential(ctx context.Context, credentialID []byte) (domain.Passkey, error) {
	r, err := q.q.GetPasskeyByCredential(ctx, b64url.EncodeToString(credentialID))
	if err != nil {
		return domain.Passkey{}, err
	}
	return passkeyFromRow(r)
}

// Passkeys lists the passkeys of an account, oldest first.
func (q Q) Passkeys(ctx context.Context, accountID domain.ID) ([]domain.Passkey, error) {
	rows, err := q.q.ListAccountPasskeys(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Passkey, 0, len(rows))
	for _, r := range rows {
		p, err := passkeyFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// TouchPasskey records a login with a passkey and its new counter.
func (q Q) TouchPasskey(ctx context.Context, id domain.ID, signCount uint32, now time.Time) error {
	return q.q.TouchPasskey(ctx, sqlc.TouchPasskeyParams{SignCount: int64(signCount), LastUsedAt: nullMillis(&now), ID: id})
}

// DeletePasskey removes a passkey from an account; false if it does not exist (or belongs to
// another account).
func (q Q) DeletePasskey(ctx context.Context, accountID, id domain.ID) (bool, error) {
	n, err := q.q.DeletePasskey(ctx, sqlc.DeletePasskeyParams{ID: id, AccountID: accountID})
	return n > 0, err
}

func passkeyFromRow(r sqlc.Passkey) (domain.Passkey, error) {
	cred, err := b64url.DecodeString(r.CredentialID)
	if err != nil {
		return domain.Passkey{}, fmt.Errorf("passkey %s: unreadable ID: %w", r.ID, err)
	}
	key, err := b64url.DecodeString(r.PublicKey)
	if err != nil {
		return domain.Passkey{}, fmt.Errorf("passkey %s: unreadable key: %w", r.ID, err)
	}
	return domain.Passkey{
		ID: r.ID, AccountID: r.AccountID, CredentialID: cred, PublicKey: key,
		SignCount: uint32(r.SignCount), //nolint:gosec // written from a uint32
		Name:      r.Name, CreatedAt: fromMillis(r.CreatedAt), LastUsedAt: optTime(r.LastUsedAt),
	}, nil
}
