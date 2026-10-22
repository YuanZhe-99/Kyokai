// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/h264/videotoolbox.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: standard-library helpers in place of sing, import paths, the decoder freed in C.

//go:build darwin && cgo

package h264

/*
#cgo LDFLAGS: -framework VideoToolbox -framework CoreMedia -framework CoreVideo -framework CoreFoundation

#include <stdlib.h>
#include <TargetConditionals.h>
#include <VideoToolbox/VideoToolbox.h>

#define VT_MAX_PARAMETER_SETS 64

typedef struct {
	VTDecompressionSessionRef session;
	CMVideoFormatDescriptionRef format;
	CVPixelBufferRef output;
	OSStatus outputStatus;
} vt_decoder;

typedef struct {
	uint8_t *luma;
	uint8_t *chroma;
	size_t lumaStride;
	size_t lumaHeight;
	size_t chromaStride;
	size_t chromaHeight;
	size_t width;
	size_t height;
} vt_planes;

static void vt_output(void *refcon, void *frameRefcon, OSStatus status, VTDecodeInfoFlags flags, CVImageBufferRef image, CMTime pts, CMTime duration) {
	vt_decoder *d = refcon;
	d->outputStatus = status;
	if (status == noErr && image != NULL) {
		if (d->output != NULL) {
			CVPixelBufferRelease(d->output);
		}
		d->output = CVPixelBufferRetain(image);
	}
}

static void vt_invalidate(vt_decoder *d) {
	if (d->session != NULL) {
		VTDecompressionSessionInvalidate(d->session);
		CFRelease(d->session);
		d->session = NULL;
	}
	if (d->format != NULL) {
		CFRelease(d->format);
		d->format = NULL;
	}
	if (d->output != NULL) {
		CVPixelBufferRelease(d->output);
		d->output = NULL;
	}
}

// vt_free ends a decoder and frees it (Kyokai: freed here, so the Go side never converts a C pointer).
static void vt_free(vt_decoder *d) {
	vt_invalidate(d);
	free(d);
}

static OSStatus vt_configure(vt_decoder *d, const uint8_t *data, const size_t *sizes, size_t count) {
	vt_invalidate(d);
	if (count > VT_MAX_PARAMETER_SETS) {
		return kCMFormatDescriptionError_InvalidParameter;
	}
	const uint8_t *sets[VT_MAX_PARAMETER_SETS];
	size_t offset = 0;
	for (size_t i = 0; i < count; i++) {
		sets[i] = data + offset;
		offset += sizes[i];
	}
	OSStatus status = CMVideoFormatDescriptionCreateFromH264ParameterSets(kCFAllocatorDefault, count, sets, sizes, 4, &d->format);
	if (status != noErr) {
		d->format = NULL;
		return status;
	}
	// VideoToolbox rescales the samples when the requested range differs from
	// the VUI video_full_range_flag; requesting the signalled range keeps them raw.
	CFTypeRef fullRange = CMFormatDescriptionGetExtension(d->format, kCMFormatDescriptionExtension_FullRangeVideo);
	int32_t pixelFormat = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange;
	if (fullRange != NULL && CFGetTypeID(fullRange) == CFBooleanGetTypeID() && CFBooleanGetValue((CFBooleanRef)fullRange)) {
		pixelFormat = kCVPixelFormatType_420YpCbCr8BiPlanarFullRange;
	}
	CFNumberRef pixelFormatNumber = CFNumberCreate(kCFAllocatorDefault, kCFNumberSInt32Type, &pixelFormat);
	const void *attributeKeys[] = {kCVPixelBufferPixelFormatTypeKey};
	const void *attributeValues[] = {pixelFormatNumber};
	CFDictionaryRef attributes = CFDictionaryCreate(kCFAllocatorDefault, attributeKeys, attributeValues, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFRelease(pixelFormatNumber);
	CFDictionaryRef specification = NULL;
#if TARGET_OS_OSX
	const void *specificationKeys[] = {kVTVideoDecoderSpecification_EnableHardwareAcceleratedVideoDecoder};
	const void *specificationValues[] = {kCFBooleanTrue};
	specification = CFDictionaryCreate(kCFAllocatorDefault, specificationKeys, specificationValues, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
#endif
	VTDecompressionOutputCallbackRecord callback = {vt_output, d};
	VTDecompressionSessionRef session = NULL;
	status = VTDecompressionSessionCreate(kCFAllocatorDefault, d->format, specification, attributes, &callback, &session);
	CFRelease(attributes);
	if (specification != NULL) {
		CFRelease(specification);
	}
	if (status != noErr) {
		vt_invalidate(d);
		return status;
	}
	d->session = session;
	VTSessionSetProperty(session, kVTDecompressionPropertyKey_RealTime, kCFBooleanTrue);
	return noErr;
}

typedef struct {
	CMBlockBufferRef block;
	uint8_t *data;
	OSStatus status;
} vt_sample_buffer;

static vt_sample_buffer vt_sample(size_t size) {
	vt_sample_buffer sample = {NULL, NULL, noErr};
	sample.status = CMBlockBufferCreateWithMemoryBlock(kCFAllocatorDefault, NULL, size, kCFAllocatorDefault, NULL, 0, size, kCMBlockBufferAssureMemoryNowFlag, &sample.block);
	if (sample.status != noErr) {
		sample.block = NULL;
		return sample;
	}
	size_t length = 0;
	char *pointer = NULL;
	sample.status = CMBlockBufferGetDataPointer(sample.block, 0, &length, NULL, &pointer);
	if (sample.status == noErr && length < size) {
		sample.status = kCMBlockBufferBadLengthParameterErr;
	}
	if (sample.status != noErr) {
		CFRelease(sample.block);
		sample.block = NULL;
		return sample;
	}
	sample.data = (uint8_t *)pointer;
	return sample;
}

typedef struct {
	void *image;
	OSStatus status;
} vt_picture;

static vt_picture vt_decode(vt_decoder *d, CMBlockBufferRef block, size_t size) {
	vt_picture picture = {NULL, noErr};
	CMSampleBufferRef sample = NULL;
	picture.status = CMSampleBufferCreateReady(kCFAllocatorDefault, block, d->format, 1, 0, NULL, 1, &size, &sample);
	CFRelease(block);
	if (picture.status != noErr) {
		return picture;
	}
	d->outputStatus = noErr;
	VTDecodeInfoFlags info = 0;
	picture.status = VTDecompressionSessionDecodeFrame(d->session, sample, 0, NULL, &info);
	if (picture.status == noErr) {
		picture.status = VTDecompressionSessionWaitForAsynchronousFrames(d->session);
	}
	CFRelease(sample);
	if (picture.status == noErr) {
		picture.status = d->outputStatus;
	}
	picture.image = d->output;
	d->output = NULL;
	return picture;
}

static int vt_lock(void *buffer, vt_planes *planes) {
	OSType format = CVPixelBufferGetPixelFormatType(buffer);
	if (format != kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange && format != kCVPixelFormatType_420YpCbCr8BiPlanarFullRange) {
		return -1;
	}
	if (CVPixelBufferGetPlaneCount(buffer) != 2) {
		return -1;
	}
	if (CVPixelBufferLockBaseAddress(buffer, kCVPixelBufferLock_ReadOnly) != kCVReturnSuccess) {
		return -1;
	}
	planes->luma = CVPixelBufferGetBaseAddressOfPlane(buffer, 0);
	planes->chroma = CVPixelBufferGetBaseAddressOfPlane(buffer, 1);
	planes->lumaStride = CVPixelBufferGetBytesPerRowOfPlane(buffer, 0);
	planes->lumaHeight = CVPixelBufferGetHeightOfPlane(buffer, 0);
	planes->chromaStride = CVPixelBufferGetBytesPerRowOfPlane(buffer, 1);
	planes->chromaHeight = CVPixelBufferGetHeightOfPlane(buffer, 1);
	planes->width = CVPixelBufferGetWidthOfPlane(buffer, 0);
	planes->height = CVPixelBufferGetHeightOfPlane(buffer, 0);
	if (planes->luma == NULL || planes->chroma == NULL) {
		CVPixelBufferUnlockBaseAddress(buffer, kCVPixelBufferLock_ReadOnly);
		return -1;
	}
	return 0;
}

static void vt_unlock(void *buffer) {
	CVPixelBufferUnlockBaseAddress(buffer, kCVPixelBufferLock_ReadOnly);
	CVPixelBufferRelease(buffer);
}
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"slices"
	"unsafe"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const lengthPrefixSize = 4

type videoSession struct {
	decoder      *C.vt_decoder
	sequenceSets [][]byte
	pictureSets  [][]byte
	sample       [][]byte
	locked       []unsafe.Pointer
}

func Available() bool {
	return true
}

func (s *videoSession) decode(units [][]byte, width, height int) (picture, bool, error) {
	var sequenceSets, pictureSets [][]byte
	hasSlice, hasIDR := false, false
	s.sample = s.sample[:0]
	sampleSize := 0
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
			s.sample = append(s.sample, unit)
			sampleSize += lengthPrefixSize + len(unit)
		}
	}
	if s.decoder == nil {
		s.decoder = (*C.vt_decoder)(C.calloc(1, C.size_t(unsafe.Sizeof(C.vt_decoder{}))))
		if s.decoder == nil {
			return picture{}, false, E.New("h264: allocate VideoToolbox decoder: out of memory")
		}
	}
	if len(sequenceSets) > 0 && !slices.EqualFunc(sequenceSets, s.sequenceSets, bytes.Equal) {
		s.sequenceSets = cloneSets(sequenceSets)
		C.vt_invalidate(s.decoder)
	}
	if len(pictureSets) > 0 && !slices.EqualFunc(pictureSets, s.pictureSets, bytes.Equal) {
		s.pictureSets = cloneSets(pictureSets)
		C.vt_invalidate(s.decoder)
	}
	// CoreMedia reports the cropped size of a format description, so the coded size that
	// VideoToolbox allocates for can only be bounded through the SPS.
	err := validateSequenceSets(s.sequenceSets, width, height)
	if err != nil {
		return picture{}, false, err
	}
	if !hasSlice {
		clear(s.sample)
		return picture{}, false, nil
	}
	if s.decoder.session == 0 {
		if len(s.sequenceSets) == 0 || len(s.pictureSets) == 0 {
			return picture{}, false, E.New("h264: slice before SPS and PPS")
		}
		if !hasIDR {
			return picture{}, false, E.New("h264: waiting for an IDR picture")
		}
		parameterSets := append(slices.Clone(s.sequenceSets), s.pictureSets...)
		data := bytes.Join(parameterSets, nil)
		sizes := make([]C.size_t, len(parameterSets))
		for i, set := range parameterSets {
			sizes[i] = C.size_t(len(set))
		}
		status := C.vt_configure(s.decoder, (*C.uint8_t)(unsafe.Pointer(&data[0])), &sizes[0], C.size_t(len(sizes)))
		if status != 0 {
			return picture{}, false, E.New("h264: create VideoToolbox session: OSStatus ", int32(status))
		}
	}
	sample := C.vt_sample(C.size_t(sampleSize))
	if sample.status != 0 {
		return picture{}, false, E.New("h264: allocate VideoToolbox sample: OSStatus ", int32(sample.status))
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(sample.data)), sampleSize)
	for _, unit := range s.sample {
		binary.BigEndian.PutUint32(data, uint32(len(unit)))
		data = data[lengthPrefixSize+copy(data[lengthPrefixSize:], unit):]
	}
	clear(s.sample)
	decoded := C.vt_decode(s.decoder, sample.block, C.size_t(sampleSize))
	buffer := decoded.image
	if decoded.status != 0 {
		if buffer != nil {
			C.CVPixelBufferRelease((*C.struct___CVBuffer)(buffer))
		}
		if decoded.status != C.kVTVideoDecoderBadDataErr {
			C.vt_invalidate(s.decoder)
		}
		return picture{}, false, E.New("h264: VideoToolbox decode: OSStatus ", int32(decoded.status))
	}
	if buffer == nil {
		return picture{}, false, E.New("h264: VideoToolbox returned no picture")
	}
	var planes C.vt_planes
	if C.vt_lock(buffer, &planes) != 0 {
		C.CVPixelBufferRelease((*C.struct___CVBuffer)(buffer))
		return picture{}, false, E.New("h264: unexpected VideoToolbox picture format")
	}
	pictureWidth, pictureHeight := uint64(planes.width), uint64(planes.height)
	chromaHeight := (pictureHeight + 1) / 2
	maxInt := uint64(^uint(0) >> 1)
	if pictureWidth == 0 || pictureHeight == 0 ||
		uint64(planes.lumaStride) < pictureWidth || uint64(planes.chromaStride) < (pictureWidth+1)&^1 ||
		uint64(planes.lumaHeight) < pictureHeight || uint64(planes.chromaHeight) < chromaHeight ||
		uint64(planes.lumaStride) > maxInt/pictureHeight || uint64(planes.chromaStride) > maxInt/chromaHeight {
		C.vt_unlock(buffer)
		return picture{}, false, E.New("h264: invalid VideoToolbox planes for a ", pictureWidth, "x", pictureHeight, " picture: luma ", uint64(planes.lumaStride), " bytes by ", uint64(planes.lumaHeight), " rows, chroma ", uint64(planes.chromaStride), " bytes by ", uint64(planes.chromaHeight), " rows")
	}
	s.locked = append(s.locked, buffer)
	lumaStride, chromaStride := int(planes.lumaStride), int(planes.chromaStride)
	return picture{
		luma:         unsafe.Slice((*byte)(unsafe.Pointer(planes.luma)), lumaStride*int(pictureHeight)),
		chroma:       unsafe.Slice((*byte)(unsafe.Pointer(planes.chroma)), chromaStride*int(chromaHeight)),
		lumaStride:   lumaStride,
		chromaStride: chromaStride,
		width:        int(pictureWidth),
		height:       int(pictureHeight),
	}, true, nil
}

func (s *videoSession) release() {
	for _, buffer := range s.locked {
		C.vt_unlock(buffer)
	}
	clear(s.locked)
	s.locked = s.locked[:0]
}

func (s *videoSession) close() {
	if s.decoder == nil {
		return
	}
	s.release()
	C.vt_free(s.decoder)
	s.decoder = nil
}

// cloneSets copies each parameter set so the session keeps them past the caller's buffer.
func cloneSets(sets [][]byte) [][]byte {
	clones := make([][]byte, len(sets))
	for i, set := range sets {
		clones[i] = bytes.Clone(set)
	}
	return clones
}
