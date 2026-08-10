// Package textutil normalizes the short, user-visible names the server accepts:
// usernames and room names. Both need the same treatment, so the rules live in
// one place.
package textutil

import (
	"fmt"
	"strings"
	"unicode"
)

// NormalizeName trims a display name, collapses internal whitespace and
// rejects anything that could be used to impersonate another name or break a
// client's layout. label is used in the error messages ("نام کاربری"، "نام روم").
func NormalizeName(raw, label string, min, max int) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("%s نمی‌تواند خالی باشد", label)
	}

	runes := []rune(name)
	if len(runes) < min {
		return "", fmt.Errorf("%s باید حداقل %d کاراکتر باشد", label, min)
	}
	if len(runes) > max {
		return "", fmt.Errorf("%s باید حداکثر %d کاراکتر باشد", label, max)
	}

	for _, r := range runes {
		switch {
		case unicode.IsControl(r):
			return "", fmt.Errorf("%s نباید کاراکتر کنترلی داشته باشد", label)
		case IsBidiControl(r):
			return "", fmt.Errorf("%s نباید کاراکتر جهت‌دهی متن داشته باشد", label)
		}
	}

	// " ali   reza " and "ali reza" must not be two different names.
	return strings.Join(strings.Fields(name), " "), nil
}

// IsBidiControl matches the LRM/RLM marks, the embedding/override controls and
// the isolate controls. They are invisible and can make one name render
// exactly like another.
func IsBidiControl(r rune) bool {
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
