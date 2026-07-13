package mcp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const cursorVersion = 1

type cursorPosition struct {
	Page   int `json:"p"`
	Offset int `json:"o,omitempty"`
}

type cursorEnvelope struct {
	Version     int    `json:"v"`
	Page        int    `json:"p"`
	Offset      int    `json:"o,omitempty"`
	Fingerprint string `json:"f"`
	Signature   string `json:"s"`
}

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

func decodeCursor(cursor string, scope any, key []byte) (cursorPosition, error) {
	if cursor == "" {
		return cursorPosition{Page: 1}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return cursorPosition{}, fmt.Errorf("invalid cursor")
	}
	var envelope cursorEnvelope
	if json.Unmarshal(data, &envelope) != nil || envelope.Version != cursorVersion || envelope.Page <= 0 || envelope.Offset < 0 {
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

func cursorSignature(envelope cursorEnvelope, key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "%d:%d:%d:%s", envelope.Version, envelope.Page, envelope.Offset, envelope.Fingerprint)
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

func cursorFingerprint(scope any) (string, error) {
	data, err := json.Marshal(scope)
	if err != nil {
		return "", fmt.Errorf("fingerprint cursor scope: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:16]), nil
}
