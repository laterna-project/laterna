package domain

import "time"

// Passkey is a WebAuthn credential of an account. It signs the user in without a password, from the
// device that holds it or any device it is synced to.
type Passkey struct {
	ID        ID
	AccountID ID
	// CredentialID is the ID given by the authenticator and PublicKey its COSE public key.
	// SignCount is the signature counter (0 for a synced passkey).
	CredentialID []byte
	PublicKey    []byte
	SignCount    uint32
	// Name given at registration ("Alex's iPhone").
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}
