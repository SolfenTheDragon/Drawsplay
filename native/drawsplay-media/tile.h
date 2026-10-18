#ifndef DRAWSPLAY_TILE_H
#define DRAWSPLAY_TILE_H

#include <stddef.h>
#include <stdint.h>

#include "common.h"

#define TILE_SIZE 64
#define TILE_MAX_PER_PACKET 256
#define TILE_HEADER_LEN 18
#define TILE_FLAG_FULL_BEGIN 1
#define TILE_FLAG_FULL_END 2
#define TILE_FRAME_HEADROOM 16

typedef struct tile_state tile_state;

tile_state *tile_open(int w, int h);
void tile_close(tile_state *t);
int tile_count(const tile_state *t);

/* Compares the BGRA frame with the previous capture inside r, updates the
 * previous capture for differing tiles and records them as dirty. Returns the
 * number of dirty tiles. */
int tile_diff(tile_state *t, const uint8_t *frame, int stride, rect_t r);
void tile_dirty_all(tile_state *t);
void tile_clear(tile_state *t);
int tile_dirty_count(const tile_state *t);

/* Iterates dirty tiles in packet-sized batches. Pass *cursor = 0 initially.
 * Returns 1 with a packet (payload pointer has TILE_FRAME_HEADROOM writable
 * bytes in front of it) or 0 when finished; -1 on error. The dirty set is
 * cleared once iteration completes. */
int tile_next_packet(tile_state *t, const uint8_t *frame, int stride, int full, int *cursor, uint8_t **payload,
                     size_t *len);

/* Inflates and applies one TILE payload to an RGB framebuffer (w*h*3). */
int tile_apply_packet(uint8_t *rgb, int w, int h, const uint8_t *payload, size_t len, int *flags);

int tile_selftest(const uint8_t *frame, int w, int h);

#endif
