//go:build android && cgo

package androidhost

/*
#cgo LDFLAGS: -lmediandk

#include <errno.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <android/hardware_buffer.h>
#include <media/NdkImageReader.h>
#include <media/NdkMediaCodec.h>

typedef struct {
	AMediaCodec *codec;
	AImageReader *reader;
	pthread_mutex_t mu;
	pthread_cond_t cond;
	int64_t rendered;
	int64_t arrived;
	int64_t pts;
	uint8_t *inBuf;
	size_t inCap;
	uint8_t *nv12;
	size_t nv12Cap;
	int width;
	int height;
} ky_mc;

static void ky_mc_arrived(void *context, AImageReader *reader) {
	ky_mc *d = context;
	pthread_mutex_lock(&d->mu);
	d->arrived++;
	pthread_cond_broadcast(&d->cond);
	pthread_mutex_unlock(&d->mu);
}

static ky_mc *ky_mc_new(void) {
	ky_mc *d = calloc(1, sizeof(ky_mc));
	if (d == NULL) {
		return NULL;
	}
	pthread_mutex_init(&d->mu, NULL);
	pthread_cond_init(&d->cond, NULL);
	return d;
}

static void ky_mc_stop(ky_mc *d) {
	if (d->reader != NULL) {
		AImageReader_setImageListener(d->reader, NULL);
	}
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

static void ky_mc_free(ky_mc *d) {
	if (d == NULL) return;
	ky_mc_stop(d);
	free(d->inBuf);
	free(d->nv12);
	pthread_cond_destroy(&d->cond);
	pthread_mutex_destroy(&d->mu);
	free(d);
}

static int ky_mc_stage(ky_mc *d, const uint8_t *src, size_t len) {
	if (len == 0) return 0;
	if (d->inCap < len) {
		uint8_t *grown = realloc(d->inBuf, len);
		if (grown == NULL) return -1;
		d->inBuf = grown;
		d->inCap = len;
	}
	memcpy(d->inBuf, src, len);
	return 0;
}

static int ky_mc_configure(
	ky_mc *d,
	const char *mime,
	const char *name,
	int width,
	int height,
	const uint8_t *csd0,
	size_t csd0Len,
	const uint8_t *csd1,
	size_t csd1Len,
	int lowLatency
) {
	ky_mc_stop(d);
	media_status_t rstat = AImageReader_newWithUsage(
		width, height, AIMAGE_FORMAT_YUV_420_888,
		AHARDWAREBUFFER_USAGE_CPU_READ_OFTEN, 4, &d->reader
	);
	if (rstat != AMEDIA_OK || d->reader == NULL) {
		d->reader = NULL;
		if (AImageReader_new(width, height, AIMAGE_FORMAT_YUV_420_888, 4, &d->reader) != AMEDIA_OK) {
			d->reader = NULL;
			return -1;
		}
	}
	AImageReader_ImageListener listener = {d, ky_mc_arrived};
	if (AImageReader_setImageListener(d->reader, &listener) != AMEDIA_OK) {
		ky_mc_stop(d);
		return -2;
	}
	ANativeWindow *window = NULL;
	if (AImageReader_getWindow(d->reader, &window) != AMEDIA_OK || window == NULL) {
		ky_mc_stop(d);
		return -3;
	}
	if (name != NULL && name[0] != '\0') {
		d->codec = AMediaCodec_createCodecByName(name);
	} else {
		d->codec = AMediaCodec_createDecoderByType(mime);
	}
	if (d->codec == NULL) {
		ky_mc_stop(d);
		return -4;
	}
	AMediaFormat *format = AMediaFormat_new();
	AMediaFormat_setString(format, AMEDIAFORMAT_KEY_MIME, mime);
	AMediaFormat_setInt32(format, AMEDIAFORMAT_KEY_WIDTH, width);
	AMediaFormat_setInt32(format, AMEDIAFORMAT_KEY_HEIGHT, height);
	if (csd0 != NULL && csd0Len > 0) {
		AMediaFormat_setBuffer(format, "csd-0", csd0, csd0Len);
	}
	if (csd1 != NULL && csd1Len > 0) {
		AMediaFormat_setBuffer(format, "csd-1", csd1, csd1Len);
	}
	if (lowLatency) {
		int32_t maxInput = width * height * 3 / 2;
		if (maxInput < (4 << 20)) maxInput = 4 << 20;
		AMediaFormat_setInt32(format, AMEDIAFORMAT_KEY_MAX_INPUT_SIZE, maxInput);
		AMediaFormat_setInt32(format, "low-latency", 1);
		AMediaFormat_setInt32(format, "priority", 0);
	}
	media_status_t status = AMediaCodec_configure(d->codec, format, window, NULL, 0);
	AMediaFormat_delete(format);
	if (status != AMEDIA_OK) {
		ky_mc_stop(d);
		return -5;
	}
	if (AMediaCodec_start(d->codec) != AMEDIA_OK) {
		ky_mc_stop(d);
		return -6;
	}
	return 0;
}

static int ky_mc_queue_staged(ky_mc *d, size_t size, uint32_t flags) {
	if (d->codec == NULL) return -10;
	if (size == 0) return 0;
	ssize_t in = AMediaCodec_dequeueInputBuffer(d->codec, 150000);
	if (in < 0) return -11;
	size_t room = 0;
	uint8_t *buf = AMediaCodec_getInputBuffer(d->codec, in, &room);
	if (buf == NULL || room < size) {
		AMediaCodec_queueInputBuffer(d->codec, in, 0, 0, 0, 0);
		return -12;
	}
	memcpy(buf, d->inBuf, size);
	d->pts += 16667;
	if (AMediaCodec_queueInputBuffer(d->codec, in, 0, size, d->pts, flags) != AMEDIA_OK) {
		return -13;
	}
	return 0;
}

static int ky_mc_copy(ky_mc *d, AImage *image, int maxWidth, int maxHeight) {
	AImageCropRect crop;
	int32_t imgW = 0, imgH = 0;
	if (AImage_getWidth(image, &imgW) != AMEDIA_OK || AImage_getHeight(image, &imgH) != AMEDIA_OK || imgW <= 0 || imgH <= 0) {
		return -20;
	}
	if (AImage_getCropRect(image, &crop) != AMEDIA_OK ||
		crop.left < 0 || crop.top < 0 ||
		crop.right <= crop.left || crop.bottom <= crop.top ||
		crop.right > imgW || crop.bottom > imgH) {
		crop.left = 0;
		crop.top = 0;
		crop.right = imgW;
		crop.bottom = imgH;
	}
	int32_t w = crop.right - crop.left;
	int32_t h = crop.bottom - crop.top;
	if (w > maxWidth) w = maxWidth;
	if (h > maxHeight) h = maxHeight;
	w &= ~1;
	h &= ~1;
	if (w <= 0 || h <= 0) return -21;

	uint8_t *y = NULL, *u = NULL, *v = NULL;
	int yLen = 0, uLen = 0, vLen = 0;
	int32_t yRow = 0, uRow = 0, vRow = 0, uPixel = 0, vPixel = 0, yPixel = 0;
	if (AImage_getPlaneData(image, 0, &y, &yLen) != AMEDIA_OK ||
		AImage_getPlaneData(image, 1, &u, &uLen) != AMEDIA_OK ||
		AImage_getPlaneData(image, 2, &v, &vLen) != AMEDIA_OK ||
		AImage_getPlaneRowStride(image, 0, &yRow) != AMEDIA_OK ||
		AImage_getPlaneRowStride(image, 1, &uRow) != AMEDIA_OK ||
		AImage_getPlaneRowStride(image, 2, &vRow) != AMEDIA_OK ||
		AImage_getPlanePixelStride(image, 0, &yPixel) != AMEDIA_OK ||
		AImage_getPlanePixelStride(image, 1, &uPixel) != AMEDIA_OK ||
		AImage_getPlanePixelStride(image, 2, &vPixel) != AMEDIA_OK ||
		y == NULL || u == NULL || v == NULL ||
		yRow <= 0 || uRow <= 0 || vRow <= 0 || uPixel <= 0 || vPixel <= 0) {
		return -23;
	}
	int64_t yLast = (int64_t)(crop.top + h - 1) * yRow + (int64_t)(crop.left + w - 1) * yPixel;
	int64_t uLast = (int64_t)((crop.top + h) / 2 - 1) * uRow + (int64_t)((crop.left + w) / 2 - 1) * uPixel;
	int64_t vLast = (int64_t)((crop.top + h) / 2 - 1) * vRow + (int64_t)((crop.left + w) / 2 - 1) * vPixel;
	if (yPixel != 1 || yLast >= yLen || uLast >= uLen || vLast >= vLen) return -24;

	size_t need = (size_t)w * h * 3 / 2;
	if (d->nv12Cap < need) {
		uint8_t *grown = realloc(d->nv12, need);
		if (grown == NULL) return -22;
		d->nv12 = grown;
		d->nv12Cap = need;
	}
	for (int row = 0; row < h; row++) {
		memcpy(d->nv12 + (size_t)row * w, y + (size_t)(crop.top + row) * yRow + crop.left, (size_t)w);
	}
	uint8_t *chroma = d->nv12 + (size_t)w * h;
	int cw = w / 2, left = crop.left / 2, top = crop.top / 2;
	for (int row = 0; row < h / 2; row++) {
		uint8_t *out = chroma + (size_t)row * w;
		const uint8_t *us = u + (size_t)(top + row) * uRow + (size_t)left * uPixel;
		const uint8_t *vs = v + (size_t)(top + row) * vRow + (size_t)left * vPixel;
		if (uPixel == 2 && vPixel == 2 && vs == us + 1) {
			if (cw > 1) {
				memcpy(out, us, (size_t)(cw - 1) * 2);
			}
			out[2 * (cw - 1)] = us[2 * (cw - 1)];
			out[2 * (cw - 1) + 1] = vs[2 * (cw - 1)];
			continue;
		}
		for (int x = 0; x < cw; x++) {
			out[2 * x] = us[x * uPixel];
			out[2 * x + 1] = vs[x * vPixel];
		}
	}
	d->width = w;
	d->height = h;
	return 1;
}

static int ky_mc_drain(ky_mc *d, int maxWidth, int maxHeight, int skipCopy) {
	if (d->codec == NULL) return -30;
	int got = 0;
	int64_t waitUs = 120000;
	for (;;) {
		AMediaCodecBufferInfo info;
		ssize_t out = AMediaCodec_dequeueOutputBuffer(d->codec, &info, got ? 0 : waitUs);
		if (out >= 0) {
			bool render = info.size > 0;
			AMediaCodec_releaseOutputBuffer(d->codec, out, render);
			if (render) {
				pthread_mutex_lock(&d->mu);
				d->rendered++;
				pthread_mutex_unlock(&d->mu);
				got = 1;
			}
			continue;
		}
		if (out == AMEDIACODEC_INFO_OUTPUT_FORMAT_CHANGED || out == AMEDIACODEC_INFO_OUTPUT_BUFFERS_CHANGED) {
			continue;
		}
		break;
	}
	if (!got) return 0;
	struct timespec until;
	clock_gettime(CLOCK_REALTIME, &until);
	until.tv_nsec += 120 * 1000000L;
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
	int rc = 1;
	if (!skipCopy) {
		rc = ky_mc_copy(d, image, maxWidth, maxHeight);
	}
	AImage_delete(image);
	return rc;
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"unsafe"
)

const flagCodecConfig = 2 // AMEDIACODEC_BUFFER_FLAG_CODEC_CONFIG

// VideoDecoder decodes H.264 or HEVC Annex B frames on Android through NDK
// AMediaCodec + AImageReader into C-owned NV12 buffers copied back to Go.
type VideoDecoder struct {
	c          *C.ky_mc
	codec      string
	mime       string
	name       string
	hardware   bool
	width      int
	height     int
	configured bool
}

// OpenVideoDecoder prepares an NDK MediaCodec decoder handle for codec
// ("h264" or "hevc"). Actual AMediaCodec configuration happens on the first
// key frame so parameter sets (csd-0 / csd-1) are supplied in AMediaFormat.
func OpenVideoDecoder(codec, codecName string, hardware bool, width, height int) (*VideoDecoder, error) {
	var mime string
	switch codec {
	case "h264":
		mime = "video/avc"
	case "hevc":
		mime = "video/hevc"
	default:
		return nil, fmt.Errorf("mediacodec: unsupported codec %q", codec)
	}
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("mediacodec: invalid dimensions %dx%d", width, height)
	}
	c := C.ky_mc_new()
	if c == nil {
		return nil, fmt.Errorf("mediacodec: out of memory")
	}
	label := codecName
	if label == "" {
		label = mime
	}
	return &VideoDecoder{
		c:        c,
		codec:    codec,
		mime:     mime,
		name:     label,
		hardware: hardware,
		width:    width,
		height:   height,
	}, nil
}

// Info returns the decoder name and whether it runs in hardware.
func (d *VideoDecoder) Info() (string, bool) {
	return d.name, d.hardware
}

func (d *VideoDecoder) configureWithCSD(csd0, csd1 []byte) error {
	cmime := C.CString(d.mime)
	defer C.free(unsafe.Pointer(cmime))
	cname := C.CString("")
	if d.name != d.mime {
		C.free(unsafe.Pointer(cname))
		cname = C.CString(d.name)
	}
	defer C.free(unsafe.Pointer(cname))

	var p0, p1 *C.uint8_t
	var n0, n1 C.size_t
	if len(csd0) > 0 {
		p0 = (*C.uint8_t)(unsafe.Pointer(&csd0[0]))
		n0 = C.size_t(len(csd0))
	}
	if len(csd1) > 0 {
		p1 = (*C.uint8_t)(unsafe.Pointer(&csd1[0]))
		n1 = C.size_t(len(csd1))
	}

	// Try low-latency configuration first, then plain AMediaFormat without vendor hints.
	rc := C.ky_mc_configure(d.c, cmime, cname, C.int(d.width), C.int(d.height), p0, n0, p1, n1, 1)
	if rc != 0 {
		rc = C.ky_mc_configure(d.c, cmime, cname, C.int(d.width), C.int(d.height), p0, n0, p1, n1, 0)
	}
	if rc != 0 && d.name != d.mime {
		empty := C.CString("")
		rc = C.ky_mc_configure(d.c, cmime, empty, C.int(d.width), C.int(d.height), p0, n0, p1, n1, 0)
		C.free(unsafe.Pointer(empty))
		if rc == 0 {
			d.name = d.mime
			d.hardware = false
		}
	}
	runtime.KeepAlive(csd0)
	runtime.KeepAlive(csd1)
	if rc != 0 {
		return fmt.Errorf("mediacodec: configure %s (%dx%d): step %d", d.codec, d.width, d.height, int(rc))
	}
	d.configured = true
	return nil
}

func (d *VideoDecoder) stageAndQueue(buf []byte, flags C.uint32_t) error {
	if len(buf) == 0 {
		return nil
	}
	rc := C.ky_mc_stage(d.c, (*C.uint8_t)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)))
	runtime.KeepAlive(buf)
	if rc != 0 {
		return fmt.Errorf("mediacodec: out of staging memory")
	}
	if rc := C.ky_mc_queue_staged(d.c, C.size_t(len(buf)), flags); rc != 0 {
		return fmt.Errorf("mediacodec: queue step %d", int(rc))
	}
	return nil
}

// Decode feeds one Annex B frame and writes NV12 (luma width*height followed by
// interleaved UV width*height/2) into dst unless skipCopy is true.
// Returns width, height, ok (true when a picture came out), error.
func (d *VideoDecoder) Decode(frame []byte, key bool, dst []byte, skipCopy bool) (int, int, bool, error) {
	if d == nil || d.c == nil || len(frame) == 0 {
		return 0, 0, false, fmt.Errorf("mediacodec: empty or closed")
	}
	if !d.configured {
		if !key {
			return 0, 0, false, nil
		}
		var csd0, csd1, rest []byte
		if d.codec == "hevc" {
			csd0, rest = SplitParameterSets(frame, true)
		} else {
			csd0, csd1, rest = SplitH264ParameterSets(frame)
		}
		if err := d.configureWithCSD(csd0, csd1); err != nil {
			return 0, 0, false, err
		}
		// If parameter sets arrived without slice data, wait for the next packet.
		if len(csd0) > 0 || len(csd1) > 0 {
			if len(rest) == 0 {
				return 0, 0, false, nil
			}
			frame = rest
		}
	} else if key {
		// On subsequent key frames, submit parameter sets (if changed) with CODEC_CONFIG
		// and slice NALs as the frame payload.
		cfg, rest := SplitParameterSets(frame, d.codec == "hevc")
		if len(cfg) > 0 && len(rest) > 0 {
			if err := d.stageAndQueue(cfg, flagCodecConfig); err != nil {
				return 0, 0, false, err
			}
			frame = rest
		} else if len(cfg) > 0 && len(rest) == 0 {
			return 0, 0, false, d.stageAndQueue(cfg, flagCodecConfig)
		}
	}

	if err := d.stageAndQueue(frame, 0); err != nil {
		return 0, 0, false, err
	}

	skip := C.int(0)
	if skipCopy {
		skip = 1
	}
	rc := C.ky_mc_drain(d.c, C.int(d.width), C.int(d.height), skip)
	switch {
	case rc == 0:
		return 0, 0, false, nil
	case rc < 0:
		return 0, 0, false, fmt.Errorf("mediacodec: drain step %d", int(rc))
	}
	if skipCopy {
		return d.width, d.height, true, nil
	}
	w, h := int(d.c.width), int(d.c.height)
	need := w * h * 3 / 2
	if w <= 0 || h <= 0 || len(dst) < need || d.c.nv12 == nil {
		return 0, 0, false, fmt.Errorf("mediacodec: invalid frame %dx%d (dst %d)", w, h, len(dst))
	}
	copy(dst[:need], unsafe.Slice((*byte)(unsafe.Pointer(d.c.nv12)), need))
	return w, h, true, nil
}

// Close stops and frees the NDK decoder, AImageReader and C buffers.
func (d *VideoDecoder) Close() {
	if d != nil && d.c != nil {
		C.ky_mc_free(d.c)
		d.c = nil
	}
}
