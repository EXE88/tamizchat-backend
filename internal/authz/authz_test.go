package authz

import "testing"

// Permission bits are stored inside role rows in the database. Reordering the
// constants would silently give every existing role a different meaning — an
// "upload files" role could become "ban users" after an upgrade. This test
// pins the numbers so that can only happen on purpose.
func TestPermissionBitsAreStable(t *testing.T) {
	want := map[string]Permission{
		"send_messages":        1,
		"moderate_chat":        2,
		"manage_rooms":         4,
		"join_locked_rooms":    8,
		"bypass_room_password": 16,
		"kick":                 32,
		"ban":                  64,
		"mute":                 128,
		"move_users":           256,
		"manage_roles":         512,
		"upload_files":         1024,
		"control_bots":         2048,
		"speak":                4096,
		"publish_video":        8192,
		"share_screen":         16384,
		"paint":                32768,
	}

	for key, bit := range want {
		d, ok := Lookup(key)
		if !ok {
			t.Fatalf("permission %q disappeared", key)
		}
		if d.Perm != bit {
			t.Fatalf("permission %q moved from %d to %d — existing roles would change meaning",
				key, bit, d.Perm)
		}
	}

	if len(All) != len(want) {
		t.Fatalf("the permission list has %d entries but %d are pinned; "+
			"add the new one here and give existing roles the bit in a migration",
			len(All), len(want))
	}
}

func TestKeysAndMaskRoundTrip(t *testing.T) {
	mask := PermKick | PermBan | PermSpeak
	keys := Keys(mask)
	if len(keys) != 3 {
		t.Fatalf("expected three keys, got %v", keys)
	}
	if got := Mask(keys); got != mask {
		t.Fatalf("round trip changed the mask: %d != %d", got, mask)
	}
}

// A client from a newer version may send a permission this server has never
// heard of; ignoring it is better than refusing the whole request.
func TestMaskIgnoresUnknownKeys(t *testing.T) {
	if got := Mask([]string{"kick", "teleport"}); got != PermKick {
		t.Fatalf("unknown keys should be dropped, got %d", got)
	}
}

func TestEverythingCoversAllPermissions(t *testing.T) {
	all := Everything()
	for _, d := range All {
		if all&d.Perm == 0 {
			t.Fatalf("Everything() is missing %q", d.Key)
		}
	}
}
