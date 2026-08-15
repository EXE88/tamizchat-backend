package httpapi

import (
	"context"
	"io"
	"os"
	"time"
)

// pacedFile serves a file at roughly the rate it plays.
//
// LiveKit's Ingress pulls a URL the way a listener would pull a radio stream,
// and it stops as soon as the stream ends. Handed a local file over a fast
// connection it read a seventy-second track in half a second, hit the end
// before its own WebRTC connection had finished connecting, and shut down having
// published nothing at all — which is what "the bot joins and no sound comes
// out" was.
//
// Reading at playing time makes a file behave like a stream: the source is still
// delivering when the connection comes up, and from then on the pipeline's own
// back-pressure keeps the two in step.
//
// It stays a ReadSeeker so http.ServeContent still handles range requests: only
// Read blocks, and a seek simply moves the budget with it.
type pacedFile struct {
	file *os.File
	ctx  context.Context

	// bytesPerSecond is the delivery rate, already including the head start.
	bytesPerSecond float64
	// burst is released immediately so playback can start without waiting.
	burst int64

	started time.Time
	sent    int64
}

// pacingSpeedup delivers faster than real time, so a player keeps buffer in hand
// and a rough duration estimate cannot starve it.
//
// Three is measured rather than guessed: Ingress buffers before it publishes,
// and at 1.5 the track took forty seconds to reach the room. What matters for
// correctness is only that delivery lasts longer than it takes the connection to
// come up, which minDelivery guarantees.
const pacingSpeedup = 3.0

// pacingBurst is how much of the *start* goes out immediately, measured in
// playing time so it scales with the track rather than the file size. A fixed
// byte count does not work: 256 KB is a fraction of a long MP3 and the whole of
// a short compressed one, which is how the first attempt at this delivered an
// entire file in one burst and changed nothing.
const pacingBurst = 5 * time.Second

// pacingFloor is the smallest burst worth sending, so a decoder always gets
// enough of the container to recognise it without waiting.
const pacingFloor = 8 << 10

// minDelivery is how long the whole body should take at the very least, so it
// outlasts the moment or two the WebRTC connection needs to come up. A track
// shorter than this is delivered over its own playing time instead — stretching
// a five-second jingle to ten would be worse than the problem.
const minDelivery = 5 * time.Second

// newPacedFile paces a file over the time it plays. A zero or unknown duration
// means no pacing at all, which is the right answer for anything whose length
// could not be worked out.
func newPacedFile(ctx context.Context, file *os.File, size int64, playing time.Duration) *pacedFile {
	rate := 0.0
	burst := int64(0)

	if playing > 0 && size > 0 {
		// How long the body should take to go out: faster than the track plays,
		// but never so fast that it lands before the connection is up.
		delivery := time.Duration(float64(playing) / pacingSpeedup)
		if delivery < minDelivery {
			delivery = min(minDelivery, playing)
		}

		rate = float64(size) / delivery.Seconds()
		burst = max(int64(rate*pacingBurst.Seconds()), pacingFloor)
	}

	return &pacedFile{
		file:           file,
		ctx:            ctx,
		bytesPerSecond: rate,
		burst:          burst,
		started:        time.Now(),
	}
}

func (p *pacedFile) Read(b []byte) (int, error) {
	if p.bytesPerSecond <= 0 {
		return p.file.Read(b)
	}

	// How much may have gone out by now, plus the head start.
	for {
		allowed := p.burst + int64(time.Since(p.started).Seconds()*p.bytesPerSecond)
		if allowed > p.sent {
			break
		}

		// Wait for the budget to cover at least one more byte rather than
		// spinning; the wait is short because the budget grows continuously.
		wait := time.Duration(float64(time.Second) / p.bytesPerSecond * 4096)
		if wait > 250*time.Millisecond {
			wait = 250 * time.Millisecond
		}
		if wait < 5*time.Millisecond {
			wait = 5 * time.Millisecond
		}

		select {
		case <-p.ctx.Done():
			// The fetcher gave up. Ending the read frees this goroutine rather
			// than leaving it sleeping through the rest of a long track.
			return 0, p.ctx.Err()
		case <-time.After(wait):
		}
	}

	allowed := p.burst + int64(time.Since(p.started).Seconds()*p.bytesPerSecond)
	if room := allowed - p.sent; int64(len(b)) > room {
		b = b[:room]
	}

	n, err := p.file.Read(b)
	p.sent += int64(n)
	return n, err
}

// Seek moves the file and the budget together, so a range request starts its
// own pacing from where it asked rather than being throttled for bytes it never
// wanted.
func (p *pacedFile) Seek(offset int64, whence int) (int64, error) {
	at, err := p.file.Seek(offset, whence)
	if err != nil {
		return at, err
	}

	p.started = time.Now()
	p.sent = 0
	return at, nil
}

var _ io.ReadSeeker = (*pacedFile)(nil)
