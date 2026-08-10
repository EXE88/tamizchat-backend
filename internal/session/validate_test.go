package session

import "testing"

func TestNormalizeUsername(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"trims", "  Ali  ", "Ali", false},
		{"collapses inner spaces", "Ali   Reza", "Ali Reza", false},
		{"persian is fine", "دانیال", "دانیال", false},
		{"emoji is fine", "ali🎧", "ali🎧", false},
		{"empty", "   ", "", true},
		{"too short", "ab", "", true},
		{"too long", "abcdefghijklmnopqrstuvwxyz", "", true},
		{"control chars", "ali\nreza", "", true},
		{"tab", "ali\treza", "", true},
		{"bidi override", "ali‮reza", "", true},
		{"rtl mark", "ali‏reza", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeUsername(tc.in, 3, 24)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeClientUUID(t *testing.T) {
	got, err := NormalizeClientUUID("  11111111-AAAA-4111-8111-111111111111 ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "11111111-aaaa-4111-8111-111111111111" {
		t.Fatalf("uuid should be trimmed and lowercased, got %q", got)
	}

	for _, bad := range []string{"", "1234", "11111111111111111111111111111111",
		"11111111-1111-4111-8111-11111111111g"} {
		if _, err := NormalizeClientUUID(bad); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}
