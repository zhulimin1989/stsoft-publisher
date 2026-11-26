package types

import (
	"strings"
	"testing"
)

func TestKeyHash_Valid(t *testing.T) {
	cases := []struct {
		name    string
		input   KeyHash
		wantErr bool
	}{
		{"empty", "", true},
		{"too-short", "abc", true},
		{"non-hex", KeyHash(strings.Repeat("z", 64)), true},
		{"upper-hex", KeyHash(strings.Repeat("A", 64)), true},
		{"raw-key-tk", "tk-1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcd", true},
		{"raw-key-sk", "sk-1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcd", true},
		{"valid", KeyHash(strings.Repeat("a", 64)), false},
		{"valid-mixed", KeyHash("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.input.Valid()
			if (err != nil) != c.wantErr {
				t.Errorf("Valid() err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}

func TestKeyID_Valid(t *testing.T) {
	cases := []struct {
		name    string
		input   KeyID
		wantErr bool
	}{
		{"empty", "", true},
		{"non-uuid", "not-a-uuid", true},
		{"v1-uuid", "550e8400-e29b-11d4-a716-446655440000", true},      // version 1
		{"valid-v4", "9f1c4b78-0b89-4e1d-8f2a-3e0d8a72b1f4", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.input.Valid()
			if (err != nil) != c.wantErr {
				t.Errorf("Valid() err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}
