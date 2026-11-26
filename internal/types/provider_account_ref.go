package types

import (
	"fmt"
	"regexp"
)

// ProviderAccountRefMaxLength bounds the opaque control-plane registry ID.
const ProviderAccountRefMaxLength = 128

// ProviderAccountRefPattern is exported so the generated JSON Schemas enforce
// the same syntax as Publisher's Go validation.
const ProviderAccountRefPattern = `^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`

var providerAccountRefRE = regexp.MustCompile(ProviderAccountRefPattern)

// ValidateProviderAccountRef validates the wire shape. Empty is allowed for
// legacy runtime payloads; strict Publisher writers and outbox evidence
// preflight separately require a non-empty value on credential-bearing lanes.
func ValidateProviderAccountRef(value string) error {
	if value == "" {
		return nil
	}
	if !providerAccountRefRE.MatchString(value) {
		return fmt.Errorf("must be an opaque stable ID matching %s", ProviderAccountRefPattern)
	}
	return nil
}
