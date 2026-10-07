package sticker

import (
	"errors"
	"strings"
	"testing"
)

// packTitle is the only ownership check available offline, so its edges matter:
// a false accept sends the bot at a set it cannot manage, and a false reject
// disables /addsticker outright.
func TestPackTitle(t *testing.T) {
	cases := []struct {
		name      string
		packName  string
		username  string
		wantTitle string
		wantErr   error
	}{
		{
			name:      "slug half becomes the title",
			packName:  "stickers_by_tiennm99bot",
			username:  "tiennm99bot",
			wantTitle: "stickers",
		},
		{
			// Telegram documents <bot_username> as case insensitive, and
			// returns SetName with whatever casing the set was created with.
			name:      "suffix matches case insensitively",
			packName:  "Shared_By_TestBot",
			username:  "testbot",
			wantTitle: "Shared",
		},
		{
			name:      "underscores inside the slug survive",
			packName:  "my_shared_pack_by_testbot",
			username:  "testbot",
			wantTitle: "my_shared_pack",
		},
		{
			name:     "another bot's set",
			packName: "shared_by_otherbot",
			username: "testbot",
			wantErr:  errPackNotBotOwned,
		},
		{
			name:     "no suffix at all",
			packName: "shared",
			username: "testbot",
			wantErr:  errPackNotBotOwned,
		},
		{
			// The suffix alone leaves no slug, and Telegram requires a name to
			// begin with a letter.
			name:     "empty slug",
			packName: "_by_testbot",
			username: "testbot",
			wantErr:  errPackNotBotOwned,
		},
		{
			name:     "past Telegram's name length cap",
			packName: strings.Repeat("a", maxSetNameLen-len("_by_testbot")+1) + "_by_testbot",
			username: "testbot",
			wantErr:  errPackNotBotOwned,
		},
		{
			// A GetMe that returned no username must not be read as "matches
			// nothing", which would report a configuration fault instead of a
			// transient one.
			name:     "unknown bot username",
			packName: "shared_by_testbot",
			username: "",
			wantErr:  errNoUsername,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := packTitle(tc.packName, tc.username)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("packTitle(%q, %q) err = %v, want %v", tc.packName, tc.username, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("packTitle(%q, %q): %v", tc.packName, tc.username, err)
			}
			if got != tc.wantTitle {
				t.Errorf("title = %q, want %q", got, tc.wantTitle)
			}
		})
	}
}

// The name is at Telegram's exact cap, which must be accepted rather than
// rejected off-by-one.
func TestPackTitle_AtNameLengthCap(t *testing.T) {
	slug := strings.Repeat("a", maxSetNameLen-len("_by_testbot"))
	got, err := packTitle(slug+"_by_testbot", "testbot")
	if err != nil {
		t.Fatalf("packTitle at the %d-char cap: %v", maxSetNameLen, err)
	}
	if got != slug {
		t.Errorf("title = %q, want %q", got, slug)
	}
}

func TestLoadStickerPack(t *testing.T) {
	t.Run("leaves the name to the default and requires an owner", func(t *testing.T) {
		t.Setenv("OWNER_ID", "42")
		t.Setenv("STICKER_PACK_NAME", "")
		pack, err := loadStickerPack()
		if err != nil {
			t.Fatalf("loadStickerPack: %v", err)
		}
		if pack.Name != "" {
			t.Errorf("name = %q, want empty until the username fills the default", pack.Name)
		}
		if pack.OwnerID != 42 {
			t.Errorf("ownerID = %d, want 42", pack.OwnerID)
		}
	})

	// A zero owner is the unset case, not a valid user: AddStickerToSet needs a
	// real account, so it must fail here rather than at the API.
	for _, owner := range []string{"", "0", "not-a-number"} {
		t.Run("rejects owner "+owner, func(t *testing.T) {
			t.Setenv("OWNER_ID", owner)
			if _, err := loadStickerPack(); !errors.Is(err, errNoPackOwner) {
				t.Errorf("loadStickerPack() err = %v, want errNoPackOwner", err)
			}
		})
	}
}

func TestIsStickerSetMissingAndOccupied(t *testing.T) {
	// isStickerSetMissing is positive-only: it authorises creating a set, so a
	// transport failure must never satisfy it.
	if isStickerSetMissing(errors.New("connection reset")) {
		t.Error("a plain error was read as a missing sticker set")
	}
	if isStickerSetMissing(errors.New("Bad Request: STICKERSET_INVALID")) {
		t.Error("a codeless error was read as a missing sticker set")
	}
	if !isPackNameOccupied(errors.New("Bad Request: PACK_SHORT_NAME_OCCUPIED")) {
		t.Error("PACK_SHORT_NAME_OCCUPIED not recognised")
	}
	if isPackNameOccupied(nil) {
		t.Error("nil error read as occupied")
	}
}
