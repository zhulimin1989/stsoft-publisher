// Package types houses the wire-shape contracts the publisher service
// exposes over HTTP and writes into Redis. These types are intentionally
// shared with both the gateway data plane (which reads them from Redis) and
// the control-plane callers (user-service / billing-service / admin-service),
// so changes here are cross-repository contract changes — see
// publisher-interface-spec.md §16 for the versioning protocol.
package types

import (
	"errors"
	"regexp"

	"github.com/google/uuid"
)

// KeyHash is the HMAC-SHA256 hex (64 lowercase chars) of the user's raw API
// key. Used as the field name in gw:keymeta and as the member of
// gw:revoked_keys. Carries no plaintext information about the user key.
type KeyHash string

// KeyID is the internal UUID v4 the platform assigns to each API key on
// creation. Used as the field name in gw:key_balance / gw:key_balance_reason
// and as the canonical identifier in events, audit logs, and limit runtime
// keys. Unlike KeyHash, KeyID has no relationship to the raw key — it is
// safe to surface in observability without privacy leakage.
type KeyID string

var (
	// ErrInvalidKeyHash is returned when a KeyHash fails validation. The
	// HTTP layer maps it to 400 INVALID_ARGUMENT.
	ErrInvalidKeyHash = errors.New("publisher: KeyHash must be 64-hex (HMAC-SHA256)")

	// ErrRawKeyDetected is returned when a KeyHash field clearly contains
	// a raw API key (e.g., starts with "tk-" or "sk-"). Defense in depth
	// against accidental raw-key submission — the publisher MUST NOT ever
	// see a raw key.
	ErrRawKeyDetected = errors.New("publisher: KeyHash looks like a raw API key (tk-/sk- prefix); refusing")

	// ErrInvalidKeyID is returned when a KeyID is not a valid UUID v4.
	ErrInvalidKeyID = errors.New("publisher: KeyID must be a UUID v4")
)

var hex64Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Valid reports whether h is the correct shape for a published HMAC-SHA256
// hex digest. Length 64, all-lowercase hex, and not obviously a raw API
// key (no tk-/sk- prefix). Empty string is rejected.
func (h KeyHash) Valid() error {
	s := string(h)
	if s == "" {
		return ErrInvalidKeyHash
	}
	// Belt-and-suspenders: any leading recognizable raw-key prefix is
	// rejected even if the rest of the string looks like hex, because
	// the API contract is that raw keys never cross this service
	// boundary. A leaked raw key arriving here means a caller bug.
	if len(s) >= 3 && (s[:3] == "tk-" || s[:3] == "sk-") {
		return ErrRawKeyDetected
	}
	if !hex64Re.MatchString(s) {
		return ErrInvalidKeyHash
	}
	return nil
}

// Valid reports whether id is a UUID v4.
func (id KeyID) Valid() error {
	s := string(id)
	if s == "" {
		return ErrInvalidKeyID
	}
	parsed, err := uuid.Parse(s)
	if err != nil {
		return ErrInvalidKeyID
	}
	if parsed.Version() != 4 {
		return ErrInvalidKeyID
	}
	return nil
}

// BatchID is the unique identifier publisher assigns to one outbox batch.
// One Publisher method call produces one batch (which may contain many
// individual Redis ops); callers can pass it back into /v1/replay to
// re-deliver a specific batch.
type BatchID = uuid.UUID
