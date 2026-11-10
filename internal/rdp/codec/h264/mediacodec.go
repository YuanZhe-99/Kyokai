// Kyokai: the videoSession on Android (the clients library of Kyokai-Mobile), through the NDK's
// MediaCodec as VideoToolbox serves macOS (videotoolbox.go). The decoder renders into an
// AImageReader surface, which every device's decoder supports, and each picture is copied out of
// its YUV_420_888 planes as NV12 for the conversions the package shares.

//go:build android && cgo

package h264

/*
#cgo LDFLAGS: -lmediandk

#include <errno.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <media/NdkImageReader.h>
#include <media/NdkMediaCodec.h>

typedef struct {
	AMediaCodec *codec;
	AImageReader *reader;
	pthread_mutex_t mu;
	pthread_cond_t cond;
	// Pictures rendered into the reader, and the ones it reported arrived.
	int64_t rendered;
	int64_t arrived;
	int64_t pts;
	// The latest picture, NV12: width bytes a luma row, as many a chroma row.
	uint8_t *nv12;
	size_t capacity;
	int width;
	int height;
} mc_decoder;

static void mc_arrived(void *context, AImageReader *reader) {
	mc_decoder *d = context;
	pthread_mutex_lock(&d->mu);
	d->arrived++;
	pthread_cond_broadcast(&d->cond);
	pthread_mutex_unlock(&d->mu);
}

static mc_decoder *mc_new(void) {
	mc_decoder *d = calloc(1, sizeof(mc_decoder));
	if (d == NULL) {
		return NULL;
	}
	pthread_mutex_init(&d->mu, NULL);
	pthread_cond_init(&d->cond, NULL);
	return d;
}

static void mc_invalidate(mc_decoder *d) {
	if (d->codec != NULL) {
		AMediaCodec_stop(d->codec);
		AMediaCodec_delete(d->codec);
		d->codec = NULL;
	}
	if (d->reader != NULL) {
		AImageReader_delete(d->reader);
		d->reader = NULL;
	}
	pthread_mutex_lock(&d->mu);
	d->rendered = d->arrived = 0;
	pthread_mutex_unlock(&d->mu);
}

static void mc_free(mc_decoder *d) {
	mc_invalidate(d);
	pthread_cond_destroy(&d->cond);
	pthread_mutex_destroy(&d->mu);
	free(d->nv12);
	free(d);
}

// mc_available reports whether this device has an H.264 decoder at all.
static int mc_available(void) {
	AMediaCodec *c = AMediaCodec_createDecoderByType("video/avc");
	if (c == NULL) {
		return 0;
	}
	AMediaCodec_delete(c);
	return 1;
}

// mc_configure opens the decoder for a width x height stream whose parameter sets are sps and
// pps, each with its start code. Returns 0, or a negative step that failed.
static int mc_configure(mc_decoder *d, int width, int height, const uint8_t *sps, size_t spsSize, const uint8_t *pps, size_t ppsSize) {
	mc_invalidate(d);
	if (AImageReader_new(width, height, AIMAGE_FORMAT_YUV_420_888, 4, &d->reader) != AMEDIA_OK) {
		d->reader = NULL;
		return -1;
	}
	AImageReader_ImageListener listener = {d, mc_arrived};
	if (AImageReader_setImageListener(d->reader, &listener) != AMEDIA_OK) {
		mc_invalidate(d);
		return -2;
	}
	ANativeWindow *window = NULL;
	if (AImageReader_getWindow(d->reader, &window) != AMEDIA_OK) {
		mc_invalidate(d);
		return -3;
	}
	d->codec = AMediaCodec_createDecoderByType("video/avc");
	if (d->codec == NULL) {
		mc_invalidate(d);
		return -4;
	}
	AMediaFormat *format = AMediaFormat_new();
	AMediaFormat_setString(format, AMEDIAFORMAT_KEY_MIME, "video/avc");
	AMediaFormat_setInt32(format, AMEDIAFORMAT_KEY_WIDTH, width);
	AMediaFormat_setInt32(format, AMEDIAFORMAT_KEY_HEIGHT, height);
	AMediaFormat_setBuffer(format, "csd-0", (void *)sps, spsSize);
	AMediaFormat_setBuffer(format, "csd-1", (void *)pps, ppsSize);
	// A picture out for every picture in, as soon as it can (where the decoder knows these).
	AMediaFormat_setInt32(format, "low-latency", 1);
	AMediaFormat_setInt32(format, "priority", 0);
	media_status_t status = AMediaCodec_configure(d->codec, format, window, NULL, 0);
	AMediaFormat_delete(format);
	if (status != AMEDIA_OK) {
		mc_invalidate(d);
		return -5;
	}
	if (AMediaCodec_start(d->codec) != AMEDIA_OK) {
		mc_invalidate(d);
		return -6;
	}
	return 0;
}

// mc_copy takes a picture out of the reader into d->nv12 (its crop, at most maxWidth x
// maxHeight). Returns 0, or a negative step that failed.
static int mc_copy(mc_decoder *d, AImage *image, int maxWidth, int maxHeight) {
	AImageCropRect crop;
	int32_t w = 0, h = 0;
	if (AImage_getCropRect(image, &crop) != AMEDIA_OK) {
		if (AImage_getWidth(image, &w) != AMEDIA_OK || AImage_getHeight(image, &h) != AMEDIA_OK) {
			return -10;
		}
		crop.left = crop.top = 0;
		crop.right = w;
		crop.bottom = h;
	}
	w = crop.right - crop.left;
	h = crop.bottom - crop.top;
	if (w > maxWidth) w = maxWidth;
	if (h > maxHeight) h = maxHeight;
	w &= ~1;
	h &= ~1;
	if (w <= 0 || h <= 0) {
		return -11;
	}
	uint8_t *y, *u, *v;
	int yLen, uLen, vLen;
	int32_t yRow, uRow, vRow, uPixel, vPixel, yPixel;
	if (AImage_getPlaneData(image, 0, &y, &yLen) != AMEDIA_OK ||
		AImage_getPlaneData(image, 1, &u, &uLen) != AMEDIA_OK ||
		AImage_getPlaneData(image, 2, &v, &vLen) != AMEDIA_OK ||
		AImage_getPlaneRowStride(image, 0, &yRow) != AMEDIA_OK ||
		AImage_getPlaneRowStride(image, 1, &uRow) != AMEDIA_OK ||
		AImage_getPlaneRowStride(image, 2, &vRow) != AMEDIA_OK ||
		AImage_getPlanePixelStride(image, 0, &yPixel) != AMEDIA_OK ||
		AImage_getPlanePixelStride(image, 1, &uPixel) != AMEDIA_OK ||
		AImage_getPlanePixelStride(image, 2, &vPixel) != AMEDIA_OK) {
		return -12;
	}
	// The planes' data starts at the image's corner, the crop inside it.
	int64_t yLast = (int64_t)(crop.top + h - 1) * yRow + (int64_t)(crop.left + w - 1) * yPixel;
	int64_t uLast = (int64_t)((crop.top + h) / 2 - 1) * uRow + (int64_t)((crop.left + w) / 2 - 1) * uPixel;
	int64_t vLast = (int64_t)((crop.top + h) / 2 - 1) * vRow + (int64_t)((crop.left + w) / 2 - 1) * vPixel;
	if (yPixel != 1 || yLast >= yLen || uLast >= uLen || vLast >= vLen) {
		return -13;
	}
	size_t need = (size_t)w * h * 3 / 2;
	if (d->capacity < need) {
		uint8_t *grown = realloc(d->nv12, need);
		if (grown == NULL) {
			return -14;
		}
		d->nv12 = grown;
		d->capacity = need;
	}
	for (int row = 0; row < h; row++) {
		memcpy(d->nv12 + (size_t)row * w, y + (size_t)(crop.top + row) * yRow + crop.left, w);
	}
	uint8_t *chroma = d->nv12 + (size_t)w * h;
	int cw = w / 2, left = crop.left / 2, top = crop.top / 2;
	for (int row = 0; row < h / 2; row++) {
		uint8_t *out = chroma + (size_t)row * w;
		const uint8_t *us = u + (size_t)(top + row) * uRow + (size_t)left * uPixel;
		const uint8_t *vs = v + (size_t)(top + row) * vRow + (size_t)left * vPixel;
		if (uPixel == 2 && vPixel == 2 && vs == us + 1) {
			memcpy(out, us, (size_t)cw * 2);
			continue;
		}
		for (int x = 0; x < cw; x++) {
			out[2 * x] = us[x * uPixel];
			out[2 * x + 1] = vs[x * vPixel];
		}
	}
	d->width = w;
	d->height = h;
	return 0;
}

// mc_decode decodes one access unit (Annex B) and copies its picture. Returns 1 with a picture,
// 0 when the decoder gave none yet, or a negative step that failed.
static int mc_decode(mc_decoder *d, const uint8_t *data, size_t size, int maxWidth, int maxHeight) {
	ssize_t in = AMediaCodec_dequeueInputBuffer(d->codec, 200000);
	if (in < 0) {
		return -20;
	}
	size_t room = 0;
	uint8_t *buffer = AMediaCodec_getInputBuffer(d->codec, in, &room);
	if (buffer == NULL || room < size) {
		AMediaCodec_queueInputBuffer(d->codec, in, 0, 0, 0, 0);
		return -21;
	}
	memcpy(buffer, data, size);
	d->pts += 1000;
	if (AMediaCodec_queueInputBuffer(d->codec, in, 0, size, d->pts, 0) != AMEDIA_OK) {
		return -22;
	}
	// The picture of this unit: decoders without reordering (RDP's streams have none) give it
	// for this input; what comes out is rendered, the newest kept.
	int got = 0;
	int64_t waitUs = 200000;
	for (;;) {
		AMediaCodecBufferInfo info;
		ssize_t out = AMediaCodec_dequeueOutputBuffer(d->codec, &info, got ? 0 : waitUs);
		if (out >= 0) {
			AMediaCodec_releaseOutputBuffer(d->codec, out, true);
			pthread_mutex_lock(&d->mu);
			d->rendered++;
			pthread_mutex_unlock(&d->mu);
			got = 1;
			continue;
		}
		if (out == AMEDIACODEC_INFO_OUTPUT_FORMAT_CHANGED || out == AMEDIACODEC_INFO_OUTPUT_BUFFERS_CHANGED) {
			continue;
		}
		break;
	}
	if (!got) {
		return 0;
	}
	struct timespec until;
	clock_gettime(CLOCK_REALTIME, &until);
	until.tv_nsec += 200 * 1000000L;
	if (until.tv_nsec >= 1000000000L) {
		until.tv_sec++;
		until.tv_nsec -= 1000000000L;
	}
	pthread_mutex_lock(&d->mu);
	while (d->arrived < d->rendered) {
		if (pthread_cond_timedwait(&d->cond, &d->mu, &until) == ETIMEDOUT) {
			break;
		}
	}
	pthread_mutex_unlock(&d->mu);
	AImage *image = NULL;
	if (AImageReader_acquireLatestImage(d->reader, &image) != AMEDIA_OK || image == NULL) {
		return 0;
	}
	int copied = mc_copy(d, image, maxWidth, maxHeight);
	AImage_delete(image);
	return copied < 0 ? copied : 1;
}
*/
import "C"

import (
	"bytes"
	"slices"
	"sync"
	"unsafe"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

type videoSession struct {
	decoder      *C.mc_decoder
	sequenceSets [][]byte
	pictureSets  [][]byte
	configured   bool
	sample       []byte
}

var available = sync.OnceValue(func() bool { return C.mc_available() != 0 })

// Available reports whether the device has an H.264 decoder, so AVC is offered only then.
func Available() bool {
	return available()
}

func (s *videoSession) decode(units [][]byte, width, height int) (picture, bool, error) {
	var sequenceSets, pictureSets [][]byte
	hasSlice, hasIDR := false, false
	s.sample = s.sample[:0]
	for _, unit := range units {
		switch unitType := unit[0] & 0x1F; unitType {
		case nalSPS:
			sequenceSets = append(sequenceSets, unit)
		case nalPPS:
			pictureSets = append(pictureSets, unit)
		case nalAUD, nalFiller:
		default:
			hasSlice = hasSlice || unitType >= nalSlice && unitType <= nalIDR
			hasIDR = hasIDR || unitType == nalIDR
			s.sample = append(append(s.sample, 0, 0, 0, 1), unit...)
		}
	}
	if s.decoder == nil {
		s.decoder = C.mc_new()
		if s.decoder == nil {
			return picture{}, false, E.New("h264: allocate MediaCodec decoder: out of memory")
		}
	}
	if len(sequenceSets) > 0 && !slices.EqualFunc(sequenceSets, s.sequenceSets, bytes.Equal) {
		s.sequenceSets = cloneSets(sequenceSets)
		s.invalidate()
	}
	if len(pictureSets) > 0 && !slices.EqualFunc(pictureSets, s.pictureSets, bytes.Equal) {
		s.pictureSets = cloneSets(pictureSets)
		s.invalidate()
	}
	err := validateSequenceSets(s.sequenceSets, width, height)
	if err != nil {
		return picture{}, false, err
	}
	if !hasSlice {
		return picture{}, false, nil
	}
	if !s.configured {
		if len(s.sequenceSets) == 0 || len(s.pictureSets) == 0 {
			return picture{}, false, E.New("h264: slice before SPS and PPS")
		}
		if !hasIDR {
			return picture{}, false, E.New("h264: waiting for an IDR picture")
		}
		sps := append([]byte{0, 0, 0, 1}, s.sequenceSets[0]...)
		pps := append([]byte{0, 0, 0, 1}, s.pictureSets[0]...)
		status := C.mc_configure(s.decoder, C.int(width), C.int(height),
			(*C.uint8_t)(unsafe.Pointer(&sps[0])), C.size_t(len(sps)),
			(*C.uint8_t)(unsafe.Pointer(&pps[0])), C.size_t(len(pps)))
		if status != 0 {
			return picture{}, false, E.New("h264: create MediaCodec decoder: step ", int(status))
		}
		s.configured = true
	}
	status := C.mc_decode(s.decoder, (*C.uint8_t)(unsafe.Pointer(&s.sample[0])), C.size_t(len(s.sample)), C.int(width), C.int(height))
	switch {
	case status == 0:
		return picture{}, false, nil
	case status < 0:
		if status <= -20 {
			// The codec itself failed: opened again at the next IDR picture.
			s.invalidate()
		}
		return picture{}, false, E.New("h264: MediaCodec decode: step ", int(status))
	}
	w, h := int(s.decoder.width), int(s.decoder.height)
	nv12 := unsafe.Slice((*byte)(unsafe.Pointer(s.decoder.nv12)), w*h*3/2)
	return picture{
		luma:         nv12[:w*h],
		chroma:       nv12[w*h:],
		lumaStride:   w,
		chromaStride: w,
		width:        w,
		height:       h,
	}, true, nil
}

func (s *videoSession) invalidate() {
	if s.decoder != nil {
		C.mc_invalidate(s.decoder)
	}
	s.configured = false
}

// release keeps the picture's buffer, which the next decode reuses.
func (s *videoSession) release() {}

func (s *videoSession) close() {
	if s.decoder == nil {
		return
	}
	C.mc_free(s.decoder)
	s.decoder = nil
	s.configured = false
}

// cloneSets copies each parameter set so the session keeps them past the caller's buffer.
func cloneSets(sets [][]byte) [][]byte {
	clones := make([][]byte, len(sets))
	for i, set := range sets {
		clones[i] = bytes.Clone(set)
	}
	return clones
}
