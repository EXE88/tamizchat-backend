package session

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// uuidPattern accepts the canonical 8-4-4-4-12 hexadecimal form. The client
// generates its UUID once at install time; anything else is a broken client.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// NormalizeClientUUID validates and canonicalizes a client identifier.
func NormalizeClientUUID(raw string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(raw))
	if !uuidPattern.MatchString(id) {
		return "", fmt.Errorf("شناسهٔ کلاینت معتبر نیست")
	}
	return id, nil
}

// NormalizeUsername trims and validates a display name. Names may contain any
// printable script — Persian included — but no control, format or combining
// trickery that would let one user impersonate another or break the layout.
func NormalizeUsername(raw string, min, max int) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("نام کاربری نمی‌تواند خالی باشد")
	}

	runes := []rune(name)
	if len(runes) < min {
		return "", fmt.Errorf("نام کاربری باید حداقل %d کاراکتر باشد", min)
	}
	if len(runes) > max {
		return "", fmt.Errorf("نام کاربری باید حداکثر %d کاراکتر باشد", max)
	}

	for _, r := range runes {
		switch {
		case unicode.IsControl(r):
			return "", fmt.Errorf("نام کاربری نباید کاراکتر کنترلی داشته باشد")
		case isBidiControl(r):
			// Bidi marks and overrides can make one name render as another.
			return "", fmt.Errorf("نام کاربری نباید کاراکتر جهت‌دهی متن داشته باشد")
		}
	}

	// Collapse internal whitespace runs so " ali   reza " cannot masquerade as
	// a different name from "ali reza".
	return strings.Join(strings.Fields(name), " "), nil
}

// isBidiControl matches the LRM/RLM marks, the embedding/override controls and
// the newer isolate controls.
func isBidiControl(r rune) bool {
	switch {
	case r == 0x200e, r == 0x200f: // LRM, RLM
		return true
	case r >= 0x202a && r <= 0x202e: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	}
	return false
}
