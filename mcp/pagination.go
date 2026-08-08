package mcp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Opaque, tamper-evident pagination cursors.
//
// A cursor is an HMAC-signed envelope, not a bare page number, for two reasons:
// it lets the server reject a cursor that was minted for a different repository
// or a different filter set (which would otherwise silently paginate the wrong
// listing), and it stops a client from steering the server by hand-crafting one.
// The key is derived from the token, so cursors do not survive a token change.

const cursorVersion = 1

// cursorPosition is a resume point inside a listing: the API page to fetch and
// how many of that page's items were already returned.
type cursorPosition struct {
	Page   int `json:"p"`
	Offset int `json:"o,omitempty"`
}

// cursorEnvelope is the wire form of a cursor. Field names are short because the
// whole struct is base64-encoded into every response.
type cursorEnvelope struct {
	Version     int    `json:"v"`
	Page        int    `json:"p"`
	Offset      int    `json:"o,omitempty"`
	Fingerprint string `json:"f"`
	Signature   string `json:"s"`
}

// valid reports whether the envelope's own fields are self-consistent, before
// any signature or scope check.
func (e cursorEnvelope) valid() bool {
	return e.Version == cursorVersion && e.Page > 0 && e.Offset >= 0
}

// cursorKey derives the HMAC key used to sign cursors. It is bound to the token,
// so a cursor cannot be replayed against a different credential.
func (s *MCPServer) cursorKey() []byte {
	digest := sha256.Sum256([]byte("github-actions-mcp cursor v1\x00" + s.config.Token))
	return digest[:]
}

// encodeCursor signs position for scope. A non-positive page yields the empty
// string, which is how "no further page" is spelled on the wire.
func encodeCursor(position cursorPosition, scope any, key []byte) (string, error) {
	if position.Page <= 0 {
		return "", nil
	}
	fingerprint, err := cursorFingerprint(scope)
	if err != nil {
		return "", err
	}
	envelope := cursorEnvelope{Version: cursorVersion, Page: position.Page, Offset: position.Offset, Fingerprint: fingerprint}
	envelope.Signature = cursorSignature(envelope, key)
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

// decodeCursor verifies a cursor against scope and key and returns its position.
// The empty cursor means "start at the first page". A cursor that is malformed,
// minted for other filters, or unsigned is rejected rather than coerced.
func decodeCursor(cursor string, scope any, key []byte) (cursorPosition, error) {
	if cursor == "" {
		return cursorPosition{Page: 1}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return cursorPosition{}, fmt.Errorf("invalid cursor")
	}
	var envelope cursorEnvelope
	if json.Unmarshal(data, &envelope) != nil || !envelope.valid() {
		return cursorPosition{}, fmt.Errorf("invalid cursor")
	}
	fingerprint, err := cursorFingerprint(scope)
	if err != nil {
		return cursorPosition{}, err
	}
	if envelope.Fingerprint != fingerprint {
		return cursorPosition{}, fmt.Errorf("cursor does not match the current repository or filters")
	}
	if !hmac.Equal([]byte(envelope.Signature), []byte(cursorSignature(envelope, key))) {
		return cursorPosition{}, fmt.Errorf("invalid cursor signature")
	}
	return cursorPosition{Page: envelope.Page, Offset: envelope.Offset}, nil
}

// cursorSignature signs the envelope's position and scope fingerprint. The
// signature field itself is excluded, and the digest is truncated to 16 bytes to
// keep cursors short.
func cursorSignature(envelope cursorEnvelope, key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "%d:%d:%d:%s", envelope.Version, envelope.Page, envelope.Offset, envelope.Fingerprint)
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// cursorFingerprint digests the listing's scope (repository plus every filter),
// so a cursor is only accepted by the exact query that produced it.
func cursorFingerprint(scope any) (string, error) {
	data, err := json.Marshal(scope)
	if err != nil {
		return "", fmt.Errorf("fingerprint cursor scope: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:16]), nil
}
