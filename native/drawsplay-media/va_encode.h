#ifndef DRAWSPLAY_VA_ENCODE_H
#define DRAWSPLAY_VA_ENCODE_H

#include <stddef.h>
#include <stdint.h>

#include "common.h"

#define VA_FRAME_HEADROOM 16

typedef struct va_enc va_enc;

typedef struct {
    char vendor[160];
    const char *entrypoint;
    const char *profile;
} va_info;

/* Opens a render node and creates an H.264 encoder. node == NULL scans
 * /dev/dri/renderD128.. and uses the first node with a working encoder. */
va_enc *va_enc_open(const char *node, int w, int h, int fps, int kbps, int gop, va_info *info);
void va_enc_close(va_enc *e);

/* Converts a BGRA rectangle (even aligned, inside the visible area) into the
 * persistent NV12 buffer and marks it for upload. */
void va_enc_convert(va_enc *e, const uint8_t *bgra, int stride, rect_t r);

void va_enc_set_bitrate(va_enc *e, int kbps);

/* Encodes one access unit from the current NV12 buffer. The AU payload has
 * VA_FRAME_HEADROOM writable bytes in front. */
int va_enc_encode(va_enc *e, int force_idr, uint8_t **au, size_t *len, int *is_idr);

/* Bit mask of NAL unit types (1 << type) found in an Annex-B buffer. */
uint32_t va_nal_mask(const uint8_t *buf, size_t len);

#endif
