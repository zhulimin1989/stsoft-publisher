package migrations_test

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/outbox"
	"github.com/tokenplatform/publisher/migrations"
)

func TestPublisherOutboxOpWidthSupportsAtomicActivation(t *testing.T) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}

	var upMigrations []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") {
			upMigrations = append(upMigrations, entry.Name())
		}
	}
	sort.Strings(upMigrations)

	columnWidth := 0
	widthPattern := regexp.MustCompile(`(?i)(?:\bop\s+|alter\s+column\s+op\s+type\s+)varchar\((\d+)\)`)
	for _, name := range upMigrations {
		content, readErr := migrations.FS.ReadFile(name)
		if readErr != nil {
			t.Fatalf("read migration %s: %v", name, readErr)
		}
		for _, match := range widthPattern.FindAllStringSubmatch(string(content), -1) {
			width, parseErr := strconv.Atoi(match[1])
			if parseErr != nil {
				t.Fatalf("parse op width in %s: %v", name, parseErr)
			}
			columnWidth = width
		}
	}

	requiredWidth := len(string(outbox.OpKeyActivateAtomic))
	if columnWidth < requiredWidth {
		t.Fatalf(
			"publisher_outbox.op width=%d cannot store %q (%d bytes)",
			columnWidth,
			outbox.OpKeyActivateAtomic,
			requiredWidth,
		)
	}
}
