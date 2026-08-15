package gateway_test

import (
	"sync"
	"testing"
	"time"
)

// fakePublisher stands in for a bot's LiveKit participant.
//
// The real one joins a room and publishes an Opus file over WebRTC, which no
// test can host. What the tests actually need to know is the same either way:
// which room the bot was put in, which file it was asked to play, and whether it
// was stopped — plus the ability to say "that track finished", which is how the
// queue advances now that nothing asks LiveKit about it.
type fakePublisher struct {
	mu sync.Mutex

	enabled bool
	rooms   map[string]string // bot id -> room id
	playing map[string]string // bot id -> file path
	done    map[string]func() // bot id -> "the track ended" callback

	joins  int
	stops  int
	leaves int
	fail   error
}

func newFakePublisher() *fakePublisher {
	return &fakePublisher{
		enabled: true,
		rooms:   map[string]string{},
		playing: map[string]string{},
		done:    map[string]func(){},
	}
}

func (f *fakePublisher) Enabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

func (f *fakePublisher) SetEnabled(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = on
}

func (f *fakePublisher) Join(botID, roomID, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.fail != nil {
		return f.fail
	}

	f.rooms[botID] = roomID
	f.joins++
	return nil
}

func (f *fakePublisher) Play(botID, path string, onDone func()) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.fail != nil {
		return f.fail
	}

	f.playing[botID] = path
	f.done[botID] = onDone
	return nil
}

func (f *fakePublisher) Stop(botID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.playing, botID)
	delete(f.done, botID)
	f.stops++
}

func (f *fakePublisher) Leave(botID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.rooms, botID)
	delete(f.playing, botID)
	delete(f.done, botID)
	f.leaves++
}

// nowPlaying is the file the bot was last asked to play, if any.
func (f *fakePublisher) nowPlaying(botID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	path, ok := f.playing[botID]
	return path, ok
}

func (f *fakePublisher) roomOf(botID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rooms[botID]
}

// finishTrack reports that the current track played to its end, the way the real
// publisher does when a file runs out.
func (f *fakePublisher) finishTrack(t *testing.T, botID string) {
	t.Helper()

	f.mu.Lock()
	done := f.done[botID]
	f.mu.Unlock()

	if done == nil {
		t.Fatalf("bot %s is not playing anything to finish", botID)
	}
	done()
}

// waitForPlaying gives the queue a moment to advance, since the end of a track
// is handled off the request path.
func (f *fakePublisher) waitForPlaying(t *testing.T, botID, want string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if path, ok := f.nowPlaying(botID); ok && path == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	got, _ := f.nowPlaying(botID)
	t.Fatalf("bot %s is playing %q, want %q", botID, got, want)
}
