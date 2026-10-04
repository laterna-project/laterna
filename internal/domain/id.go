// Package domain holds the core types. It does no I/O: no database, no HTTP, no file system. Every
// other layer depends on it and it depends on none of them.
package domain

import (
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"uuid"
)

// ID identifies an entity (item, account, server...) for good. IDs are UUIDv7, so they sort by
// creation time and keep indexes compact. They are written in the canonical UUID form (36
// characters, lower case, with dashes).
type ID [16]byte

// NewID returns a new ID.
func NewID() ID { return ID(uuid.NewV7()) }

// ErrInvalidID is returned for a malformed ID.
var ErrInvalidID = errors.New("invalid ID")

// ParseID accepts the canonical form (36 characters with dashes) and the compact one (32 hex
// digits), in any case.
func ParseID(s string) (ID, error) {
	switch len(s) {
	case 32:
		var id ID
		if _, err := hex.Decode(id[:], []byte(s)); err != nil {
			return ID{}, ErrInvalidID
		}
		return id, nil
	case 36:
		u, err := uuid.Parse(strings.ToLower(s))
		if err != nil {
			return ID{}, ErrInvalidID
		}
		return ID(u), nil
	default:
		return ID{}, ErrInvalidID
	}
}

// String returns the canonical form.
func (id ID) String() string { return uuid.UUID(id).String() }

// IsZero reports an ID that was never set.
func (id ID) IsZero() bool { return id == ID{} }

// MarshalText writes the canonical form (JSON, TOML...).
func (id ID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// Scan reads an ID from the database (16-byte BLOB).
func (id *ID) Scan(src any) error {
	b, ok := src.([]byte)
	if !ok || len(b) != len(id) {
		return fmt.Errorf("ID: %T value of %d bytes, 16 expected", src, len(b))
	}
	copy(id[:], b)
	return nil
}

// Value writes the ID to the database (16-byte BLOB).
func (id ID) Value() (driver.Value, error) { return id[:], nil }

// UnmarshalText reads the canonical or the compact form.
func (id *ID) UnmarshalText(b []byte) error {
	parsed, err := ParseID(string(b))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}
