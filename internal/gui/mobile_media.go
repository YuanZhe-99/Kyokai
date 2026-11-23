package gui

import (
	"bytes"
	"encoding/json"
	"image"
	"image/draw"
	"image/jpeg"
	"log"
	"runtime"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/androidhost"
	"github.com/YuanZhe-99/Kyokai/internal/core"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

func (v *viewer) decodeVideoAndroid(lib *core.Lib, video <-chan []byte, pool packetPool, keyRequest []byte) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var mcDec *androidhost.VideoDecoder
	var rawDec *core.Decoder
	closeAll := func() {
		if mcDec != nil {
			mcDec.Close()
			mcDec = nil
		}
		if rawDec != nil {
			rawDec.Close()
			rawDec = nil
		}
	}
	defer closeAll()

	fresh := false
	fedSinceKey := 0
	var firstKeyAt time.Time
	usedFallback := false
	v.rate = decodeRate{}

	openFor := func(s videoStart, rawJSON []byte, forceSoftware bool) bool {
		closeAll()
		switch s.Codec {
		case "h264", "hevc":
			name := ""
			hw := false
			if forceSoftware {
				if s.Codec == "hevc" {
					name = "c2.android.hevc.decoder"
				} else {
					name = "c2.android.avc.decoder"
				}
			} else if v.app.mobile != nil {
				for _, cap := range v.app.mobile.decoders {
					if cap.Codec == s.Codec {
						name, hw = cap.Name, cap.Hardware
						break
					}
				}
			}
			d, err := androidhost.OpenVideoDecoder(s.Codec, name, hw, s.Width, s.Height)
			if err != nil && name != "" {
				d, err = androidhost.OpenVideoDecoder(s.Codec, "", false, s.Width, s.Height)
			}
			if err != nil {
				log.Printf("android video: open %s (%dx%d): %v", s.Codec, s.Width, s.Height, err)
				return false
			}
			mcDec = d
			return true
		case "mjpeg":
			return true
		default:
			d, err := lib.OpenDecoder(rawJSON)
			if err != nil {
				return false
			}
			rawDec = d
			return true
		}
	}

	for p := range video {
		switch p[0] {
		case packetVideoStart:
			var s videoStart
			if json.Unmarshal(p[1:], &s) != nil || s.Width <= 0 || s.Height <= 0 || s.Width > 16384 || s.Height > 16384 {
				break
			}
			v.mu.Lock()
			same := (mcDec != nil || rawDec != nil || s.Codec == "mjpeg") && v.start == s
			v.mu.Unlock()
			if same {
				break
			}
			usedFallback = false
			if !openFor(s, p[1:], false) {
				v.setState(L.ViewerUnsupported(codecLabel(s.Codec)))
				v.gate.restart()
				break
			}
			fresh = s.Codec != "mjpeg"
			fedSinceKey = 0
			firstKeyAt = time.Time{}
			if s.Codec == "h264" || s.Codec == "hevc" {
				need := ((s.Width + 1) &^ 1) * ((s.Height + 1) &^ 1) * 3 / 2
				var bufs [3][]byte
				for i := range 3 {
					bufs[i] = make([]byte, need)
				}
				name, hw := mcDec.Info()
				v.mu.Lock()
				v.start, v.nv12s, v.nv12Mode, v.shown, v.ready = s, bufs, true, -1, -1
				v.decoder = codecImpl{name, hw}
				v.streamBegan, v.firstFrameMS = time.Now(), 0
				v.mu.Unlock()
				v.setState("")
			} else {
				v.begin(s)
				if s.Codec == "mjpeg" {
					v.mu.Lock()
					v.decoder = codecImpl{"jpeg", false}
					v.mu.Unlock()
				}
			}

		case packetFrame:
			if len(p) < 10 {
				break
			}
			isKey := p[1]&keyFrame != 0
			v.mu.Lock()
			codec := v.start.Codec
			sw, sh := v.start.Width, v.start.Height
			v.mu.Unlock()

			if fresh && !isKey && codec != "mjpeg" {
				break
			}
			data := p[10:]
			behind := len(video) > 0

			switch {
			case mcDec != nil:
				if fresh {
					firstKeyAt = time.Now()
					fedSinceKey = 0
					fresh = false
				}
				v.mu.Lock()
				free := 0
				for free == v.shown || free == v.ready {
					free++
				}
				dst := v.nv12s[free]
				v.bytes += len(data)
				fpsCap := v.fpsCap
				v.mu.Unlock()

				began := time.Now()
				w, h, ok, err := mcDec.Decode(data, isKey, dst, behind)
				fedSinceKey++
				if err != nil {
					log.Printf("android video: decode %s (%dx%d, key=%v): %v", codec, sw, sh, isKey, err)
					fresh = true
					v.mu.Lock()
					v.decodeFailed = true
					v.mu.Unlock()
					if !usedFallback {
						usedFallback = true
						if openFor(videoStart{Codec: codec, Width: sw, Height: sh}, nil, true) && mcDec != nil {
							name, hw := mcDec.Info()
							v.mu.Lock()
							v.decoder = codecImpl{name, hw}
							v.mu.Unlock()
						}
					}
					if v.gate.broke(time.Now()) {
						v.askKey(keyRequest)
					}
					break
				}
				if !ok {
					if !usedFallback && !firstKeyAt.IsZero() && fedSinceKey >= 5 && time.Since(firstKeyAt) > 4*time.Second {
						usedFallback = true
						fresh = true
						_ = openFor(videoStart{Codec: codec, Width: sw, Height: sh}, nil, true)
						v.askKey(keyRequest)
					}
					break
				}
				decoded := time.Now()
				limit, changed := v.rate.add(decoded.Sub(began), decoded, capOf(fpsCap))
				v.mu.Lock()
				if v.firstFrameMS == 0 && !v.streamBegan.IsZero() {
					v.firstFrameMS = decoded.Sub(v.streamBegan).Milliseconds()
				}
				v.frames++
				v.pictureAt = decoded
				v.decodeFailed = false
				v.decoding += decoded.Sub(began)
				if !behind {
					v.nv12Sizes[free] = [2]int{w, h}
					v.ready, v.readyAt = free, decoded
				}
				if el := decoded.Sub(v.counted); el >= time.Second {
					v.fps = float64(v.frames) / el.Seconds()
					v.kbps = float64(v.bytes) * 8 / 1000 / el.Seconds()
					v.decodeMS = v.decoding.Seconds() * 1000 / float64(max(v.frames, 1))
					v.drawMS = v.drawing.Seconds() * 1000 / float64(max(v.drawn, 1))
					v.frames, v.drawn, v.bytes, v.counted = 0, 0, 0, decoded
					v.decoding, v.drawing = 0, 0
				}
				if changed {
					v.fpsLimit = limit
				}
				v.mu.Unlock()
				if changed {
					v.sendQuality()
				}
				if !behind {
					v.update(v.present)
				}

			case codec == "mjpeg":
				if behind {
					break
				}
				src, err := jpeg.Decode(bytes.NewReader(data))
				if err != nil {
					break
				}
				b := src.Bounds()
				v.mu.Lock()
				free := 0
				for free == v.shown || free == v.ready {
					free++
				}
				pic := v.imgs[free]
				if pic == nil || pic.Rect.Dx() != b.Dx() || pic.Rect.Dy() != b.Dy() {
					pic = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
					v.imgs[free] = pic
				}
				v.bytes += len(data)
				v.mu.Unlock()
				draw.Draw(pic, pic.Bounds(), src, b.Min, draw.Src)
				decoded := time.Now()
				v.mu.Lock()
				if v.firstFrameMS == 0 && !v.streamBegan.IsZero() {
					v.firstFrameMS = decoded.Sub(v.streamBegan).Milliseconds()
				}
				v.frames++
				v.pictureAt = decoded
				v.ready, v.readyAt = free, decoded
				v.mu.Unlock()
				v.update(v.present)

			case rawDec != nil:
				first := fresh
				fresh = false
				if v.show(rawDec, data, behind) != nil {
					fresh = true
					v.mu.Lock()
					v.decodeFailed = true
					v.mu.Unlock()
					if v.gate.broke(time.Now()) {
						v.askKey(keyRequest)
					}
				} else if first {
					v.mu.Lock()
					if v.firstFrameMS == 0 && !v.streamBegan.IsZero() {
						v.firstFrameMS = time.Since(v.streamBegan).Milliseconds()
					}
					v.mu.Unlock()
				}
			}
		}
		pool.put(p)
	}
}

func (v *viewer) playSoundAndroid(sound <-chan []byte, pool packetPool) {
	var snd androidhost.SoundPlayer
	defer func() {
		if snd != nil {
			_ = snd.Close()
		}
	}()
	for p := range sound {
		v.mu.Lock()
		mute := v.mute
		v.mu.Unlock()
		switch {
		case mute && snd != nil:
			_ = snd.Close()
			snd = nil
		case !mute && snd == nil && v.app.mobile != nil && v.app.mobile.host != nil:
			snd, _ = v.app.mobile.host.OpenSound()
		}
		if snd != nil && len(p) > 1 {
			snd.Feed(p[1:])
		}
		pool.put(p)
	}
}
