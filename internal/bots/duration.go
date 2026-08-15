package bots

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TrackDuration reports how long a track plays, when it can be worked out
// cheaply from the file's own headers.
//
// It exists because the music has to be *served* at roughly the rate it plays.
// LiveKit's Ingress pulls a URL expecting a stream; handed a local file over a
// fast connection it drains the whole thing in milliseconds, reaches the end
// before its WebRTC connection is even up, and ends the session having published
// nothing. Serving at playing time is what makes a file behave like a stream.
//
// Only the headers are read — never the whole file — and anything unrecognised
// returns false, which the caller treats as "send it as fast as you like".
func TrackDuration(path string) (time.Duration, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.Size() <= 0 {
		return 0, false
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".ogg", ".oga", ".opus":
		return oggDuration(file, info.Size())
	case ".flac":
		return flacDuration(file)
	case ".wav":
		return wavDuration(file)
	case ".mp3":
		return mp3Duration(file, info.Size())
	default:
		return 0, false
	}
}

// oggDuration reads the granule position of the last page, which is the sample
// count of the whole stream, and the sample rate from the first header packet.
func oggDuration(file *os.File, size int64) (time.Duration, bool) {
	rate, channels, ok := oggHeader(file)
	if !ok || rate <= 0 {
		return 0, false
	}
	_ = channels

	// The last page is at the end, so only the tail is read. 64 KB is far more
	// than a page, which is capped at about 64 KB by the format itself.
	const tail = 1 << 16
	start := max(size-tail, 0)

	buf := make([]byte, size-start)
	if _, err := file.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return 0, false
	}

	granule := int64(-1)
	for i := len(buf) - 27; i >= 0; i-- {
		if buf[i] == 'O' && buf[i+1] == 'g' && buf[i+2] == 'g' && buf[i+3] == 'S' {
			granule = int64(binary.LittleEndian.Uint64(buf[i+6 : i+14]))
			break
		}
	}
	if granule <= 0 {
		return 0, false
	}

	return time.Duration(granule) * time.Second / time.Duration(rate), true
}

// oggHeader pulls the sample rate out of the first Vorbis or Opus header, both
// of which sit in the first page.
func oggHeader(file *os.File) (rate, channels int, ok bool) {
	head := make([]byte, 1024)
	n, err := file.ReadAt(head, 0)
	if n < 64 || (err != nil && !errors.Is(err, io.EOF)) {
		return 0, 0, false
	}
	head = head[:n]

	if i := indexOf(head, []byte("\x01vorbis")); i >= 0 && i+16 < len(head) {
		// vorbis identification: version(4) channels(1) rate(4)
		channels = int(head[i+11])
		rate = int(binary.LittleEndian.Uint32(head[i+12 : i+16]))
		return rate, channels, rate > 0
	}

	if i := indexOf(head, []byte("OpusHead")); i >= 0 && i+16 < len(head) {
		channels = int(head[i+9])
		// Opus granule positions are always counted at 48 kHz, whatever the
		// original sample rate in the header says.
		return 48000, channels, true
	}

	return 0, 0, false
}

// flacDuration reads STREAMINFO: the sample rate and total sample count.
func flacDuration(file *os.File) (time.Duration, bool) {
	head := make([]byte, 42)
	if n, err := file.ReadAt(head, 0); n < 42 || (err != nil && !errors.Is(err, io.EOF)) {
		return 0, false
	}
	if string(head[:4]) != "fLaC" {
		return 0, false
	}

	// STREAMINFO starts at 8; sample rate is 20 bits and total samples 36 bits,
	// packed across bytes 18..27 of the file.
	b := head[18:28]
	rate := int(b[0])<<12 | int(b[1])<<4 | int(b[2])>>4
	total := int64(b[3]&0x0f)<<32 | int64(b[4])<<24 | int64(b[5])<<16 |
		int64(b[6])<<8 | int64(b[7])

	if rate <= 0 || total <= 0 {
		return 0, false
	}
	return time.Duration(total) * time.Second / time.Duration(rate), true
}

// wavDuration divides the data chunk by the byte rate in the format chunk.
func wavDuration(file *os.File) (time.Duration, bool) {
	head := make([]byte, 64)
	if n, err := file.ReadAt(head, 0); n < 44 || (err != nil && !errors.Is(err, io.EOF)) {
		return 0, false
	}
	if string(head[:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return 0, false
	}

	byteRate := int64(binary.LittleEndian.Uint32(head[28:32]))
	dataSize := int64(binary.LittleEndian.Uint32(head[40:44]))
	if byteRate <= 0 || dataSize <= 0 {
		return 0, false
	}
	return time.Duration(dataSize) * time.Second / time.Duration(byteRate), true
}

// mp3Duration estimates from the bitrate of the first frame.
//
// An estimate is enough: pacing only needs to be roughly right, and a variable
// bitrate file that is served slightly fast or slow still behaves like a stream.
func mp3Duration(file *os.File, size int64) (time.Duration, bool) {
	head := make([]byte, 8192)
	n, err := file.ReadAt(head, 0)
	if n < 4 || (err != nil && !errors.Is(err, io.EOF)) {
		return 0, false
	}
	head = head[:n]

	// Skip an ID3v2 tag, whose size is 4 syncsafe bytes at offset 6.
	offset := 0
	if n > 10 && string(head[:3]) == "ID3" {
		tag := int(head[6]&0x7f)<<21 | int(head[7]&0x7f)<<14 |
			int(head[8]&0x7f)<<7 | int(head[9]&0x7f)
		offset = 10 + tag
	}

	for i := offset; i+1 < len(head); i++ {
		if head[i] != 0xff || head[i+1]&0xe0 != 0xe0 {
			continue
		}
		if i+3 >= len(head) {
			break
		}

		bitrate, ok := mp3Bitrate(head[i], head[i+1], head[i+2])
		if !ok {
			continue
		}

		// Bytes at that many bits per second, minus whatever the tag took.
		payload := size - int64(offset)
		return time.Duration(payload*8) * time.Second / time.Duration(bitrate), true
	}

	return 0, false
}

// mp3Bitrate decodes the frame header's bitrate field, in bits per second.
func mp3Bitrate(b1, b2, b3 byte) (int, bool) {
	// MPEG version: 3 = MPEG1, 2 = MPEG2, 0 = MPEG2.5
	version := (b2 >> 3) & 0x03
	layer := (b2 >> 1) & 0x03
	index := (b3 >> 4) & 0x0f

	if version == 1 || layer == 0 || index == 0 || index == 0x0f {
		return 0, false
	}

	// Layer III only, which is what an .mp3 is. The tables are in kbit/s.
	mpeg1 := []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	mpeg2 := []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}

	table := mpeg2
	if version == 3 {
		table = mpeg1
	}
	if int(index) >= len(table) {
		return 0, false
	}
	return table[index] * 1000, true
}

func indexOf(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
