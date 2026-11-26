package dbinit

import (
	"context"
	"testing"
)

func TestValidIdent(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"plain", "publisher", true},
		{"with_underscore", "my_publisher_db", true},
		{"with_digits", "db_2026", true},
		{"upper", "PublisherDB", true},
		{"max_len", string(make([]byte, 63)), false}, // zero bytes, not letters
		{"empty", "", false},
		{"too_long", repeat("a", 64), false},
		{"exactly_63", repeat("a", 63), true},
		{"hyphen_rejected", "publisher-db", false},
		{"dot_rejected", "publisher.db", false},
		{"semicolon_injection", "publisher;DROP TABLE x", false},
		{"quote_injection", `publisher" CASCADE; --`, false},
		{"space_rejected", "publisher db", false},
		{"unicode_rejected", "publishér", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validIdent(tc.input); got != tc.want {
				t.Errorf("validIdent(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func TestEnsureDatabase_InvalidDSN(t *testing.T) {
	if err := EnsureDatabase(context.TODO(), "not a url"); err == nil {
		t.Fatal("expected error for malformed dsn")
	}
}

func TestEnsureDatabase_DSNWithoutDatabase(t *testing.T) {
	// keyword/value DSN with no dbname set — pgconn.ParseConfig returns
	// empty Database; EnsureDatabase must refuse rather than CREATE "".
	if err := EnsureDatabase(context.TODO(), "host=127.0.0.1 port=5432 user=x password=y sslmode=disable"); err == nil {
		t.Fatal("expected error when DSN has no database name")
	}
}
