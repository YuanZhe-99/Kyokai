package gui

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/egoist/mygo/audio"

	"github.com/YuanZhe-99/Kyokai/internal/core"
)

// Sound of the host: 48 kHz stereo, as the core decodes it (opus::RATE).
const (
	soundRate     = 48000
	soundChannels = 2
	// The sound kept ahead of what was played, in frames: a target that
	// starts at soundMin, grows by soundGrow each time playing ran dry while
	// sound came (the network's jitter), up to soundMax, and shrinks by
	// soundShrink after soundCalm without that. Sound beyond it (a burst,
	// a host whose clock runs fast) is left out, oldest first, so the sound
	// stays with the picture.
	soundMin    = soundRate * 40 / 1000
	soundMax    = soundRate * 300 / 1000
	soundGrow   = soundRate * 20 / 1000
	soundShrink = soundRate * 10 / 1000
	soundCalm   = 10 * time.Second
	// soundGap: a pause longer than this is the host's silence, not jitter.
	soundGap = 300 * time.Millisecond
	// soundPacket is room for one decoded packet (Opus' longest, 120 ms).
	soundPacket = soundRate * 120 / 1000 * soundChannels
)

// liveSound is the host's sound as MyGo's player reads it (audio.Decoder):
// what has arrived, up to an adaptive target ahead of what was played.
type liveSound struct {
	mu   sync.Mutex
	cond sync.Cond
	buf  []float32
	// handed: the frames given to the player; played reports the frames it
	// played (its position), so the difference is what waits to be heard.
	handed int64
	played func() int64
	now    func() time.Time
	// target is the frames kept ahead; last when sound last came; calm
	// since when playing has not run dry.
	target     int64
	last, calm time.Time
	closed     bool
}

func newLiveSound(played func() int64, now func() time.Time) *liveSound {
	s := &liveSound{played: played, now: now, target: soundMin}
	s.cond.L = &s.mu
	return s
}

// feed adds decoded sound and keeps what waits to be heard within the
// target: playing that ran dry raises it, a calm spell lowers it, and the
// oldest sound not handed yet goes beyond it.
func (s *liveSound) feed(pcm []float32) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	played := s.played()
	waiting := s.handed + int64(len(s.buf)/soundChannels) - played
	switch {
	case s.last.IsZero():
		s.calm = now
	case waiting <= 0 && now.Sub(s.last) < soundGap:
		s.target, s.calm = min(s.target+soundGrow, soundMax), now
	case now.Sub(s.calm) > soundCalm:
		s.target, s.calm = max(s.target-soundShrink, soundMin), now
	}
	s.last = now
	s.buf = append(s.buf, pcm...)
	if over := s.handed + int64(len(s.buf)/soundChannels) - played - s.target; over > 0 {
		drop := int(min(over, int64(len(s.buf)/soundChannels))) * soundChannels
		s.buf = append(s.buf[:0], s.buf[drop:]...)
	}
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
	}, time.Now)
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
