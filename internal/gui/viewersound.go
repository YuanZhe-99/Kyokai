package gui

import (
	"errors"
	"io"
	"sync"

	"github.com/egoist/mygo/audio"

	"github.com/YuanZhe-99/Kyokai/internal/core"
)

// Sound of the host: 48 kHz stereo, as the core decodes it (opus::RATE).
const (
	soundRate     = 48000
	soundChannels = 2
	// soundLag is the most sound kept ahead of what was played: more comes
	// from a burst or a host whose clock runs fast, and is left out, so the
	// sound stays with the picture.
	soundLag = soundRate * 150 / 1000
	// soundPacket is room for one decoded packet (Opus' longest, 120 ms).
	soundPacket = soundRate * 120 / 1000 * soundChannels
)

// liveSound is the host's sound as MyGo's player reads it (audio.Decoder):
// what has arrived, at most soundLag ahead of what was played.
type liveSound struct {
	mu   sync.Mutex
	cond sync.Cond
	buf  []float32
	// handed: the frames given to the player; played reports the frames it
	// played (its position), so the difference is what waits to be heard.
	handed int64
	played func() int64
	closed bool
}

func newLiveSound(played func() int64) *liveSound {
	s := &liveSound{played: played}
	s.cond.L = &s.mu
	return s
}

// feed adds decoded sound, unless as much waits to be heard already.
func (s *liveSound) feed(pcm []float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiting := s.handed + int64(len(s.buf)/soundChannels) - s.played()
	if s.closed || waiting > soundLag {
		return
	}
	s.buf = append(s.buf, pcm...)
	s.cond.Signal()
}

// close ends the sound: the player's next read ends it.
func (s *liveSound) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cond.Broadcast()
}

// SampleRate is the sound's frames per second.
func (s *liveSound) SampleRate() int { return soundRate }

// Channels is the sound's channels: stereo.
func (s *liveSound) Channels() int { return soundChannels }

// Read waits for sound and hands over what arrived, whole frames.
func (s *liveSound) Read(buf []float32) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.buf) == 0 && !s.closed {
		s.cond.Wait()
	}
	if s.closed {
		return 0, io.EOF
	}
	n := copy(buf[:len(buf)/soundChannels*soundChannels], s.buf)
	n -= n % soundChannels
	s.buf = append(s.buf[:0], s.buf[n:]...)
	s.handed += int64(n / soundChannels)
	return n, nil
}

// SetPosition cannot move live sound.
func (s *liveSound) SetPosition(int64) error { return errors.New("live sound") }

// Len is unknown: the sound goes on.
func (s *liveSound) Len() int64 { return -1 }

// viewerSound plays one viewer's host sound.
type viewerSound struct {
	dec    *core.AudioDecoder
	live   *liveSound
	player *audio.Player
	pcm    []float32
}

// openSound starts playing the host's sound.
// Returns: an error when the core has no sound decoder.
func openSound(lib *core.Lib) (*viewerSound, error) {
	dec, err := lib.OpenAudio()
	if err != nil {
		return nil, err
	}
	s := &viewerSound{dec: dec, pcm: make([]float32, soundPacket)}
	var player *audio.Player
	s.live = newLiveSound(func() int64 {
		return int64(player.Position().Seconds() * soundRate)
	})
	player = audio.NewPlayer(s.live)
	s.player = player
	player.Play()
	return s, nil
}

// packet decodes and plays one Opus packet; a bad one is left out.
func (s *viewerSound) packet(p []byte) {
	n, err := s.dec.Decode(p, s.pcm)
	if err == nil && n > 0 {
		s.live.feed(s.pcm[:n*soundChannels])
	}
}

// close stops the sound and releases its decoder.
func (s *viewerSound) close() {
	s.live.close()
	s.player.Stop()
	s.dec.Close()
}
