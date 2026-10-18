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
	// starts at soundStart, grows by soundGrow each time playing ran dry
	// while sound came (the network's jitter), up to soundMax, and shrinks
	// by soundShrink after soundCalm without that, down to soundMin.
	soundStart  = soundRate * 80 / 1000
	soundMin    = soundRate * 40 / 1000
	soundMax    = soundRate * 400 / 1000
	soundGrow   = soundRate * 40 / 1000
	soundShrink = soundRate * 10 / 1000
	soundCalm   = 10 * time.Second
	// soundSlack: sound ahead beyond target+soundSlack (a burst, a host
	// whose clock runs fast) is cut back to the target, oldest first, so
	// the sound stays with the picture.
	soundSlack = soundRate * 60 / 1000
	// soundDry: no more than this ahead is playing that ran dry (the
	// player's interpolation keeps a few frames).
	soundDry = soundRate / 1000
	// soundGap: a pause longer than this is the host's silence, not jitter.
	soundGap = 300 * time.Millisecond
	// soundPacket is room for one decoded packet (Opus' longest, 120 ms).
	soundPacket = soundRate * 120 / 1000 * soundChannels
)

// liveSound is the host's sound as MyGo's player reads it (audio.Decoder),
// a jitter buffer: it holds sound back until the target is buffered, at the
// start and whenever playing ran dry, then hands it on as it comes.
type liveSound struct {
	mu   sync.Mutex
	cond sync.Cond
	buf  []float32
	// handed: the frames given to the player; played reports the frames it
	// played (its position), so the difference is what it holds.
	handed int64
	played func() int64
	now    func() time.Time
	// target is the frames kept ahead; filling while they are gathered;
	// last when sound last came; calm since when playing has not run dry.
	target     int64
	filling    bool
	last, calm time.Time
	closed     bool
}

func newLiveSound(played func() int64, now func() time.Time) *liveSound {
	s := &liveSound{played: played, now: now, target: soundStart, filling: true}
	s.cond.L = &s.mu
	return s
}

// feed adds decoded sound. Playing that ran dry gathers the target again
// (a larger one when it was jitter, not the host's silence); a calm spell
// lowers the target; sound far beyond it is cut back.
// Side effects: wakes the player's Read once sound may be handed on.
func (s *liveSound) feed(pcm []float32) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	held := s.handed - s.played()
	switch {
	case !s.filling && held <= soundDry && len(s.buf) == 0:
		s.filling = true
		if now.Sub(s.last) < soundGap {
			s.target, s.calm = min(s.target+soundGrow, soundMax), now
		}
	case s.calm.IsZero():
		s.calm = now
	case now.Sub(s.calm) > soundCalm:
		s.target, s.calm = max(s.target-soundShrink, soundMin), now
	}
	s.last = now
	s.buf = append(s.buf, pcm...)
	ahead := held + int64(len(s.buf)/soundChannels)
	if over := ahead - s.target; s.filling && over > 0 || over > soundSlack {
		drop := int(min(over, int64(len(s.buf)/soundChannels))) * soundChannels
		s.buf = append(s.buf[:0], s.buf[drop:]...)
		ahead -= int64(drop / soundChannels)
	}
	if s.filling && ahead >= s.target {
		s.filling = false
	}
	if !s.filling {
		s.cond.Signal()
	}
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

// Read waits until sound may be handed on and hands over what arrived,
// whole frames.
func (s *liveSound) Read(buf []float32) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for (s.filling || len(s.buf) == 0) && !s.closed {
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
