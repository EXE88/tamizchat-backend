package session

import (
	"fmt"
	"regexp"
	"strings"

	"tamizchat/internal/textutil"
)

// uuidPattern accepts the canonical 8-4-4-4-12 hexadecimal form. The client
// generates its UUID once at install time; anything else is a broken client.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// NormalizeClientUUID validates and canonicalizes a client identifier.
func NormalizeClientUUID(raw string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(raw))
	if !uuidPattern.MatchString(id) {
		return "", fmt.Errorf("the client ID is not valid")
	}
	return id, nil
}

// NormalizeUsername trims and validates a display name. Names may contain any
// printable script — Persian included — but no control or bidi trickery.
func NormalizeUsername(raw string, min, max int) (string, error) {
	return textutil.NormalizeName(raw, "username", min, max)
}
