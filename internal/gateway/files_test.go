package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tamizchat/internal/config"
	"tamizchat/internal/protocol"
)

// upload runs the whole flow a client does: ask for a ticket, POST the bytes,
// and return the attachment the server stored.
func (f *fixture) upload(t *testing.T, c *client, name string, body []byte) protocol.Attachment {
	t.Helper()

	c.send(protocol.TypeFileUploadRequest, "u1", protocol.FileUploadRequest{
		Name: name, Size: int64(len(body)),
	})
	var ticket protocol.FileUploadTicket
	c.decode(c.expect(protocol.TypeFileUploadTicket), &ticket)

	resp := f.postUpload(t, ticket.Token, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload failed: %d %s", resp.StatusCode, raw)
	}

	var attachment protocol.Attachment
	decodeJSON(t, resp, &attachment)
	return attachment
}

func (f *fixture) postUpload(t *testing.T, token string, body []byte) *http.Response {
	t.Helper()
	resp, err := http.Post(f.httpURL+"/api/v1/upload?token="+token,
		"application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post upload: %v", err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, into any) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode body %q: %v", raw, err)
	}
}

// pngBytes builds a small solid-colour PNG.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// roomWith connects an admin, creates a room and joins it.
func (f *fixture) roomWith(t *testing.T) (*client, protocol.Room) {
	t.Helper()
	alice, _ := f.hello(t, uuidA, "Alice", "")
	room := alice.createRoom("Lobby", "", 0)
	alice.joinRoom(room.ID, "")
	return alice, room
}

func TestUploadBecomesAMessageInTheRoom(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)

	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	// A non-ASCII file name on purpose: names round-trip as UTF-8 and must not
	// be mangled on the way through the upload ticket.
	attachment := f.upload(t, alice, "گزارش.txt", []byte("hello file"))
	if attachment.Name != "گزارش.txt" || attachment.Size != 10 {
		t.Fatalf("unexpected attachment: %+v", attachment)
	}
	if attachment.Kind != protocol.AttachmentFile {
		t.Fatalf("a text file is not an image: %+v", attachment)
	}

	// Both the uploader and the other members are told, because the upload was
	// confirmed over HTTP rather than over the socket.
	for _, c := range []*client{alice, bob} {
		var msg protocol.Message
		c.decode(c.expect(protocol.TypeChatMessage), &msg)
		if msg.Kind != protocol.MessageFile || msg.Attachment == nil {
			t.Fatalf("expected a file message, got %+v", msg)
		}
		if msg.Attachment.ID != attachment.ID || msg.Author.ClientUUID != uuidA {
			t.Fatalf("unexpected file message: %+v", msg)
		}
	}
}

func TestUploadedFileCanBeDownloaded(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)
	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	content := []byte("the quick brown fox")
	attachment := f.upload(t, alice, "note.txt", content)
	alice.expect(protocol.TypeChatMessage)
	bob.expect(protocol.TypeChatMessage)

	// Another member of the room asks for a link and follows it.
	bob.send(protocol.TypeFileDownloadToken, "d1", protocol.FileDownloadRequest{FileID: attachment.ID})
	var link protocol.FileDownload
	bob.decode(bob.expect(protocol.TypeFileDownload), &link)
	if link.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("the link should not be expired already: %+v", link)
	}

	resp, err := http.Get(f.httpURL + link.URL)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download status %d", resp.StatusCode)
	}

	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, content) {
		t.Fatalf("downloaded %q, want %q", got, content)
	}

	// An uploaded file must never be served in a way a browser would execute.
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("downloads must be sent with nosniff")
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("downloads must be attachments, got %q", resp.Header.Get("Content-Disposition"))
	}
}

func TestDownloadNeedsAValidToken(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.roomWith(t)

	attachment := f.upload(t, alice, "secret.txt", []byte("classified"))
	alice.expect(protocol.TypeChatMessage)

	for _, url := range []string{
		f.httpURL + "/api/v1/file/" + attachment.ID,
		f.httpURL + "/api/v1/file/" + attachment.ID + "?token=made-up",
	} {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%s should not have been served", url)
		}
	}
}

// A file id leaked out of one room must not be fetchable from another.
func TestDownloadIsScopedToTheRoom(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.roomWith(t)
	other := alice.createRoom("Other", "", 0)

	attachment := f.upload(t, alice, "note.txt", []byte("room one only"))
	alice.expect(protocol.TypeChatMessage)

	bob, _ := f.hello(t, uuidB, "Bob", "")
	alice.expect(protocol.TypeUserJoined)
	bob.joinRoom(other.ID, "")
	alice.expect(protocol.TypeRoomMemberJoined)

	bob.send(protocol.TypeFileDownloadToken, "d1", protocol.FileDownloadRequest{FileID: attachment.ID})
	bob.expectError(protocol.ErrFileNotFound)
}

func TestImageGetsDimensionsAndThumbnail(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.roomWith(t)

	attachment := f.upload(t, alice, "photo.png", pngBytes(t, 600, 300))
	alice.expect(protocol.TypeChatMessage)

	if attachment.Kind != protocol.AttachmentImage {
		t.Fatalf("a PNG should be recognised as an image: %+v", attachment)
	}
	if attachment.MIME != "image/png" {
		t.Fatalf("unexpected mime: %q", attachment.MIME)
	}
	if attachment.Width != 600 || attachment.Height != 300 {
		t.Fatalf("unexpected dimensions: %dx%d", attachment.Width, attachment.Height)
	}
	if !attachment.HasThumb {
		t.Fatal("an image should get a thumbnail")
	}

	alice.send(protocol.TypeFileDownloadToken, "d1", protocol.FileDownloadRequest{FileID: attachment.ID})
	var link protocol.FileDownload
	alice.decode(alice.expect(protocol.TypeFileDownload), &link)
	if link.ThumbURL == "" {
		t.Fatal("an image link should include its thumbnail")
	}

	resp, err := http.Get(f.httpURL + link.ThumbURL)
	if err != nil {
		t.Fatalf("get thumb: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("thumb status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("thumbnails are JPEG, got %q", ct)
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		t.Fatalf("decode thumb: %v", err)
	}
	maxPx := f.cfg.Int(config.KeyUploadsThumbMaxPx)
	if img.Bounds().Dx() > maxPx || img.Bounds().Dy() > maxPx {
		t.Fatalf("thumbnail is too big: %v", img.Bounds())
	}
	// 600x300 scaled to fit 320 keeps the aspect ratio.
	if img.Bounds().Dx() != 320 || img.Bounds().Dy() != 160 {
		t.Fatalf("aspect ratio was not preserved: %v", img.Bounds())
	}
}

// The declared content type is irrelevant: only the bytes decide.
func TestMIMEComesFromTheBytesNotTheName(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.roomWith(t)

	attachment := f.upload(t, alice, "totally-an-image.png", []byte("just plain text, honest"))
	alice.expect(protocol.TypeChatMessage)

	if attachment.Kind != protocol.AttachmentFile {
		t.Fatalf("a text file named .png is still a file: %+v", attachment)
	}
	if strings.HasPrefix(attachment.MIME, "image/") {
		t.Fatalf("detected mime should not be an image: %q", attachment.MIME)
	}
}

func TestOversizedUploadIsRefusedUpFront(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyUploadsMaxSizeMB, "1"); err != nil {
		t.Fatalf("set max size: %v", err)
	}
	alice, _ := f.roomWith(t)

	alice.send(protocol.TypeFileUploadRequest, "u1", protocol.FileUploadRequest{
		Name: "big.bin", Size: 2 << 20,
	})
	alice.expectError(protocol.ErrFileTooLarge)
}

// Declaring a small size and then sending more must still be caught.
func TestOversizedBodyIsRefusedWhileStreaming(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyUploadsMaxSizeMB, "1"); err != nil {
		t.Fatalf("set max size: %v", err)
	}
	alice, _ := f.roomWith(t)

	alice.send(protocol.TypeFileUploadRequest, "u1", protocol.FileUploadRequest{
		Name: "liar.bin", Size: 10,
	})
	var ticket protocol.FileUploadTicket
	alice.decode(alice.expect(protocol.TypeFileUploadTicket), &ticket)

	resp := f.postUpload(t, ticket.Token, bytes.Repeat([]byte("x"), (1<<20)+1024))
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
}

func TestUploadRequiresPermission(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)

	f.defaultRolePermissionsWithout(t, "upload_files")

	bob := f.inRoom(t, uuidB, "Bob", room.ID)
	alice.expect(protocol.TypeUserJoined)
	alice.expect(protocol.TypeRoomMemberJoined)

	bob.send(protocol.TypeFileUploadRequest, "u1", protocol.FileUploadRequest{
		Name: "x.txt", Size: 3,
	})
	bob.expectError(protocol.ErrForbidden)
}

func TestUploadRequiresBeingInARoom(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.hello(t, uuidA, "Alice", "")

	alice.send(protocol.TypeFileUploadRequest, "u1", protocol.FileUploadRequest{
		Name: "x.txt", Size: 3,
	})
	alice.expectError(protocol.ErrNotInRoom)
}

func TestUploadsCanBeDisabled(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyUploadsEnabled, "false"); err != nil {
		t.Fatalf("disable uploads: %v", err)
	}
	alice, _ := f.roomWith(t)

	alice.send(protocol.TypeFileUploadRequest, "u1", protocol.FileUploadRequest{
		Name: "x.txt", Size: 3,
	})
	alice.expectError(protocol.ErrUploadsDisabled)
}

func TestTicketIsSingleUse(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.roomWith(t)

	alice.send(protocol.TypeFileUploadRequest, "u1", protocol.FileUploadRequest{
		Name: "once.txt", Size: 4,
	})
	var ticket protocol.FileUploadTicket
	alice.decode(alice.expect(protocol.TypeFileUploadTicket), &ticket)

	first := f.postUpload(t, ticket.Token, []byte("once"))
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first upload should succeed, got %d", first.StatusCode)
	}
	alice.expect(protocol.TypeChatMessage)

	second := f.postUpload(t, ticket.Token, []byte("again"))
	second.Body.Close()
	if second.StatusCode != http.StatusForbidden {
		t.Fatalf("a spent ticket should be refused, got %d", second.StatusCode)
	}
}

func TestRoomQuotaIsEnforced(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.cfg.Set(ctx, config.KeyUploadsRoomQuotaMB, "1"); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	alice, _ := f.roomWith(t)

	// Fill most of the megabyte.
	f.upload(t, alice, "chunk.bin", bytes.Repeat([]byte("x"), 900<<10))
	alice.expect(protocol.TypeChatMessage)

	alice.send(protocol.TypeFileUploadRequest, "u2", protocol.FileUploadRequest{
		Name: "toomuch.bin", Size: 200 << 10,
	})
	alice.expectError(protocol.ErrRoomQuotaFull)
}

// The heart of the design: when the room empties, the bytes are deleted from
// disk, not merely forgotten.
func TestFilesAreDeletedWhenTheRoomIsPurged(t *testing.T) {
	f := newFixture(t)
	if err := f.cfg.Set(context.Background(), config.KeyRoomsPurgeGraceSec, "0"); err != nil {
		t.Fatalf("set grace: %v", err)
	}

	alice, room := f.roomWith(t)
	attachment := f.upload(t, alice, "photo.png", pngBytes(t, 100, 100))
	alice.expect(protocol.TypeChatMessage)

	dir := filepath.Join(f.cfg.String(config.KeyUploadsDir), room.ID)
	if entries, err := os.ReadDir(dir); err != nil || len(entries) == 0 {
		t.Fatalf("the file should be on disk: %v", err)
	}

	alice.send(protocol.TypeRoomLeave, "l1", nil)
	alice.expectFrames(protocol.TypeRoomLeft, protocol.TypeRoomPurged)

	if _, err := os.ReadDir(dir); !os.IsNotExist(err) {
		t.Fatalf("the room's directory should be gone, got %v", err)
	}

	used, count := f.files.RoomUsage(room.ID)
	if used != 0 || count != 0 {
		t.Fatalf("quota should be released, got %d bytes in %d files", used, count)
	}

	// And the file itself no longer resolves.
	alice.joinRoom(room.ID, "")
	alice.send(protocol.TypeFileDownloadToken, "d1", protocol.FileDownloadRequest{FileID: attachment.ID})
	alice.expectError(protocol.ErrFileNotFound)
}

func TestFilesAreDeletedWithTheRoom(t *testing.T) {
	f := newFixture(t)
	alice, room := f.roomWith(t)
	f.upload(t, alice, "note.txt", []byte("bye"))
	alice.expect(protocol.TypeChatMessage)

	dir := filepath.Join(f.cfg.String(config.KeyUploadsDir), room.ID)

	alice.send(protocol.TypeRoomDelete, "d1", protocol.RoomDelete{RoomID: room.ID})
	alice.expectFrames(protocol.TypeRoomLeft, protocol.TypeRoomDeleted)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.ReadDir(dir); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deleting a room should delete its files")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFileNamesAreSanitized(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.roomWith(t)

	attachment := f.upload(t, alice, `../../etc/passwd`, []byte("x"))
	alice.expect(protocol.TypeChatMessage)
	if attachment.Name != "passwd" {
		t.Fatalf("path components should be stripped, got %q", attachment.Name)
	}

	alice.send(protocol.TypeFileUploadRequest, "u2", protocol.FileUploadRequest{Name: "   "})
	alice.expectError(protocol.ErrFileInvalid)
}
