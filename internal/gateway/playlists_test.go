package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tamizchat/internal/protocol"
)

// createBotOverSocket makes a bot the way an admin in the client does.
func (c *client) createBotOverSocket(name string) protocol.Bot {
	c.t.Helper()
	c.send(protocol.TypeBotCreate, "bc", protocol.BotSpec{Name: &name})
	var view protocol.Bot
	c.decode(c.expect(protocol.TypeBotState), &view)
	return view
}

// createPlaylist adds a playlist to a bot and returns it.
func (c *client) createPlaylist(botID, name string) protocol.BotPlaylist {
	c.t.Helper()
	c.send(protocol.TypeBotPlaylistCreate, "pc", protocol.BotPlaylistSpec{
		BotID: botID, Name: name,
	})
	var list protocol.BotPlaylist
	c.decode(c.expect(protocol.TypeBotPlaylist), &list)
	return list
}

// uploadTrack walks the whole two-step upload: a ticket over the socket, then
// the bytes over HTTP, exactly as a client does.
func (f *fixture) uploadTrack(t *testing.T, c *client, botID, playlistID, name string,
	body []byte) (*http.Response, protocol.Bot) {
	t.Helper()

	c.send(protocol.TypeBotTrackUpload, "tu", protocol.BotTrackUploadRequest{
		BotID: botID, PlaylistID: playlistID, Name: name, Size: int64(len(body)),
	})
	var ticket protocol.BotTrackUploadTicket
	c.decode(c.expect(protocol.TypeBotTrackUploadInfo), &ticket)

	resp, err := http.Post(f.httpURL+ticket.URL, "application/octet-stream",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post track: %v", err)
	}
	defer resp.Body.Close()

	var view protocol.Bot
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
			t.Fatalf("decode upload reply: %v", err)
		}
	}
	return resp, view
}

func TestPlaylistLifecycleAndUpload(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	bot := alice.createBotOverSocket("DJ")
	list := alice.createPlaylist(bot.ID, "Party")
	if list.Name != "Party" || list.BotID != bot.ID {
		t.Fatalf("playlist came back wrong: %+v", list)
	}

	resp, view := f.uploadTrack(t, alice, bot.ID, list.ID, "one.ogg", []byte("fake audio"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", resp.StatusCode)
	}
	// The bot is still playing its own (empty) folder, so the queue has not
	// changed: the track is in the playlist, not yet in what plays.
	if view.TrackCount != 0 {
		t.Fatalf("an unselected playlist must not change the queue: %+v", view)
	}

	alice.send(protocol.TypeBotPlaylistSelect, "ps", protocol.BotPlaylistSpec{
		BotID: bot.ID, PlaylistID: list.ID,
	})
	var selected protocol.Bot
	alice.decode(alice.expect(protocol.TypeBotState), &selected)
	if selected.PlaylistID != list.ID || selected.PlaylistName != "Party" {
		t.Fatalf("the bot is not on the playlist: %+v", selected)
	}
	if selected.TrackCount != 1 {
		t.Fatalf("track count = %d, want 1", selected.TrackCount)
	}

	alice.send(protocol.TypeBotQueue, "q1", protocol.BotRequest{BotID: bot.ID})
	var queue protocol.BotQueue
	alice.decode(alice.expect(protocol.TypeBotQueueReply), &queue)
	if len(queue.Tracks) != 1 || queue.Tracks[0].Title != "one" {
		t.Fatalf("queue = %+v, want the uploaded track", queue.Tracks)
	}

	alice.send(protocol.TypeBotPlaylistList, "pl", protocol.BotRequest{BotID: bot.ID})
	var playlists protocol.BotPlaylistList
	alice.decode(alice.expect(protocol.TypeBotPlaylists), &playlists)
	if len(playlists.Playlists) != 1 || playlists.Playlists[0].TrackCount != 1 {
		t.Fatalf("playlist listing = %+v", playlists.Playlists)
	}
	if playlists.Active != list.ID {
		t.Fatalf("active playlist = %q, want %q", playlists.Active, list.ID)
	}

	alice.send(protocol.TypeBotTrackDelete, "td", protocol.BotTrackRef{
		BotID: bot.ID, PlaylistID: list.ID, Index: 0,
	})
	var emptied protocol.Bot
	alice.decode(alice.expect(protocol.TypeBotState), &emptied)
	if emptied.TrackCount != 0 {
		t.Fatalf("the track was not removed: %+v", emptied)
	}

	alice.send(protocol.TypeBotPlaylistDelete, "pd", protocol.BotPlaylistSpec{
		BotID: bot.ID, PlaylistID: list.ID,
	})
	// Deleting the active playlist puts the bot back on its own library. Alice
	// is the actor, so she is left out of the bot.state broadcast and gets only
	// her own confirmation.
	alice.expect(protocol.TypeBotPlaylistGone)

	alice.send(protocol.TypeBotPlaylistList, "pl2", protocol.BotRequest{BotID: bot.ID})
	alice.decode(alice.expect(protocol.TypeBotPlaylists), &playlists)
	if len(playlists.Playlists) != 0 || playlists.Active != "" {
		t.Fatalf("the playlist is still there: %+v", playlists)
	}
}

func TestUploadRejectsWhatIsNotMusic(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	bot := alice.createBotOverSocket("DJ")
	list := alice.createPlaylist(bot.ID, "Party")

	// The extension is what the scanner goes by, so a name it would never pick
	// up is refused at the ticket rather than stored and silently ignored.
	alice.send(protocol.TypeBotTrackUpload, "t1", protocol.BotTrackUploadRequest{
		BotID: bot.ID, PlaylistID: list.ID, Name: "notes.txt",
	})
	alice.expectError(protocol.ErrTrackNotAudio)

	// A name climbing out of the folder keeps only its base, which is not audio.
	alice.send(protocol.TypeBotTrackUpload, "t2", protocol.BotTrackUploadRequest{
		BotID: bot.ID, PlaylistID: list.ID, Name: "../../etc/passwd",
	})
	alice.expectError(protocol.ErrTrackNotAudio)

	// A traversal attempt ending in an accepted extension must still land inside
	// the playlist folder.
	resp, _ := f.uploadTrack(t, alice, bot.ID, list.ID, "../escape.ogg", []byte("fake audio"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", resp.StatusCode)
	}

	stored, err := f.store.GetBot(context.Background(), bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	// stored.Folder is the bot's library; its storage root is one level up, and
	// the playlist is a folder in there.
	root := filepath.Dir(stored.Folder)
	for _, escaped := range []string{
		filepath.Join(root, "escape.ogg"),
		filepath.Join(filepath.Dir(root), "escape.ogg"),
		filepath.Join(stored.Folder, "escape.ogg"),
	} {
		if _, err := os.Stat(escaped); !os.IsNotExist(err) {
			t.Fatalf("a track escaped its playlist folder: %s", escaped)
		}
	}
	if _, err := os.Stat(filepath.Join(root, list.ID, "escape.ogg")); err != nil {
		t.Fatalf("the track should be inside the playlist folder: %v", err)
	}
}

func TestPlaylistTicketIsSingleUse(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	bot := alice.createBotOverSocket("DJ")
	list := alice.createPlaylist(bot.ID, "Party")

	alice.send(protocol.TypeBotTrackUpload, "tu", protocol.BotTrackUploadRequest{
		BotID: bot.ID, PlaylistID: list.ID, Name: "one.ogg",
	})
	var ticket protocol.BotTrackUploadTicket
	alice.decode(alice.expect(protocol.TypeBotTrackUploadInfo), &ticket)

	for i, want := range []int{http.StatusOK, http.StatusForbidden} {
		resp, err := http.Post(f.httpURL+ticket.URL, "application/octet-stream",
			strings.NewReader("fake audio"))
		if err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("post %d gave %d, want %d", i, resp.StatusCode, want)
		}
	}
}

func TestPlaylistsNeedManageBots(t *testing.T) {
	f := newFixture(t)
	bot := f.addBot(t, "DJ", musicFolder(t, "one.ogg"))
	f.defaultRolePermissionsWith(t, "control_bots")
	bob, _ := f.hello(t, uuidB, "Bob", "")

	bob.send(protocol.TypeBotPlaylistList, "p1", protocol.BotRequest{BotID: bot.ID})
	bob.expectError(protocol.ErrForbidden)

	bob.send(protocol.TypeBotPlaylistCreate, "p2", protocol.BotPlaylistSpec{
		BotID: bot.ID, Name: "Mine",
	})
	bob.expectError(protocol.ErrForbidden)

	bob.send(protocol.TypeBotTrackUpload, "p3", protocol.BotTrackUploadRequest{
		BotID: bot.ID, PlaylistID: "whatever", Name: "one.ogg",
	})
	bob.expectError(protocol.ErrForbidden)

	// The queue itself is public, like the bot list.
	bob.send(protocol.TypeBotQueue, "q1", protocol.BotRequest{BotID: bot.ID})
	bob.expect(protocol.TypeBotQueueReply)
}

func TestPlaylistOfAnotherBotIsNotReachable(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	one := alice.createBotOverSocket("One")
	two := alice.createBotOverSocket("Two")
	list := alice.createPlaylist(one.ID, "Party")

	// The playlist id is real, but it belongs to the other bot: an id must never
	// become a way to write into another bot's folder.
	alice.send(protocol.TypeBotTrackUpload, "t1", protocol.BotTrackUploadRequest{
		BotID: two.ID, PlaylistID: list.ID, Name: "one.ogg",
	})
	alice.expectError(protocol.ErrPlaylistNotFound)

	alice.send(protocol.TypeBotPlaylistSelect, "s1", protocol.BotPlaylistSpec{
		BotID: two.ID, PlaylistID: list.ID,
	})
	alice.expectError(protocol.ErrPlaylistNotFound)
}

func TestDeletingABotTakesItsPlaylists(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	bot := alice.createBotOverSocket("DJ")
	list := alice.createPlaylist(bot.ID, "Party")
	f.uploadTrack(t, alice, bot.ID, list.ID, "one.ogg", []byte("fake audio"))

	stored, err := f.store.GetBot(context.Background(), bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}

	alice.send(protocol.TypeBotDelete, "d1", protocol.BotRef{BotID: bot.ID})
	alice.expect(protocol.TypeBotGone)

	if _, err := os.Stat(stored.Folder); !os.IsNotExist(err) {
		t.Fatalf("the bot's music should be gone, stat gave %v", err)
	}
	rows, err := f.store.ListPlaylists(context.Background())
	if err != nil {
		t.Fatalf("list playlists: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("playlist rows outlived their bot: %+v", rows)
	}
}
