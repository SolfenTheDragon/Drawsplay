#include "tile.h"

#include <stdlib.h>
#include <string.h>
#include <zlib.h>

struct tile_state {
    int w, h, tw, th;
    uint8_t *prev;
    uint8_t *dirty;
    int ndirty;
    uint32_t frame_id;

    uint8_t *raw;
    size_t raw_cap;
    uint8_t *out;
    size_t out_cap;
    z_stream zs;
};

static void put16(uint8_t *p, unsigned v)
{
    p[0] = (uint8_t)v;
    p[1] = (uint8_t)(v >> 8);
}

static void put32(uint8_t *p, uint32_t v)
{
    put16(p, v & 0xffff);
    put16(p + 2, v >> 16);
}

tile_state *tile_open(int w, int h)
{
    tile_state *t = calloc(1, sizeof *t);
    if (!t)
        return NULL;
    t->w = w;
    t->h = h;
    t->tw = (w + TILE_SIZE - 1) / TILE_SIZE;
    t->th = (h + TILE_SIZE - 1) / TILE_SIZE;
    t->prev = calloc((size_t)w * h, 4);
    t->dirty = calloc((size_t)t->tw * t->th, 1);
    t->raw_cap = (size_t)TILE_MAX_PER_PACKET * (4 + TILE_SIZE * TILE_SIZE * 3);
    t->raw = malloc(t->raw_cap);
    t->out_cap = TILE_FRAME_HEADROOM + TILE_HEADER_LEN + compressBound(t->raw_cap);
    t->out = malloc(t->out_cap);
    if (!t->prev || !t->dirty || !t->raw || !t->out || deflateInit(&t->zs, 1) != Z_OK) {
        free(t->prev);
        free(t->dirty);
        free(t->raw);
        free(t->out);
        free(t);
        return NULL;
    }
    return t;
}

void tile_close(tile_state *t)
{
    if (!t)
        return;
    deflateEnd(&t->zs);
    free(t->prev);
    free(t->dirty);
    free(t->raw);
    free(t->out);
    free(t);
}

int tile_count(const tile_state *t)
{
    return t->tw * t->th;
}

int tile_dirty_count(const tile_state *t)
{
    return t->ndirty;
}

void tile_dirty_all(tile_state *t)
{
    memset(t->dirty, 1, (size_t)t->tw * t->th);
    t->ndirty = t->tw * t->th;
}

void tile_clear(tile_state *t)
{
    memset(t->dirty, 0, (size_t)t->tw * t->th);
    t->ndirty = 0;
}

int tile_diff(tile_state *t, const uint8_t *frame, int stride, rect_t r)
{
    r = rect_clip(r, t->w, t->h);
    if (rect_empty(&r))
        return t->ndirty;
    int tx0 = r.x / TILE_SIZE, tx1 = (r.x + r.w - 1) / TILE_SIZE;
    int ty0 = r.y / TILE_SIZE, ty1 = (r.y + r.h - 1) / TILE_SIZE;
    for (int ty = ty0; ty <= ty1; ty++) {
        for (int tx = tx0; tx <= tx1; tx++) {
            int x0 = tx * TILE_SIZE, y0 = ty * TILE_SIZE;
            int x1 = x0 + TILE_SIZE > t->w ? t->w : x0 + TILE_SIZE;
            int y1 = y0 + TILE_SIZE > t->h ? t->h : y0 + TILE_SIZE;
            size_t bytes = (size_t)(x1 - x0) * 4;
            int changed = 0;
            for (int y = y0; y < y1; y++) {
                if (memcmp(frame + (size_t)y * stride + (size_t)x0 * 4, t->prev + ((size_t)y * t->w + x0) * 4,
                           bytes) != 0) {
                    changed = 1;
                    break;
                }
            }
            if (!changed)
                continue;
            for (int y = y0; y < y1; y++)
                memcpy(t->prev + ((size_t)y * t->w + x0) * 4, frame + (size_t)y * stride + (size_t)x0 * 4, bytes);
            if (!t->dirty[ty * t->tw + tx]) {
                t->dirty[ty * t->tw + tx] = 1;
                t->ndirty++;
            }
        }
    }
    return t->ndirty;
}

int tile_next_packet(tile_state *t, const uint8_t *frame, int stride, int full, int *cursor, uint8_t **payload,
                     size_t *len)
{
    int total = t->tw * t->th;
    int idx = *cursor;
    while (idx < total && !t->dirty[idx])
        idx++;
    if (idx >= total) {
        memset(t->dirty, 0, (size_t)total);
        t->ndirty = 0;
        return 0;
    }

    uint8_t *p = t->raw;
    int n = 0;
    while (idx < total && n < TILE_MAX_PER_PACKET) {
        if (!t->dirty[idx]) {
            idx++;
            continue;
        }
        int tx = idx % t->tw, ty = idx / t->tw;
        int x0 = tx * TILE_SIZE, y0 = ty * TILE_SIZE;
        int x1 = x0 + TILE_SIZE > t->w ? t->w : x0 + TILE_SIZE;
        int y1 = y0 + TILE_SIZE > t->h ? t->h : y0 + TILE_SIZE;
        put16(p, (unsigned)tx);
        put16(p + 2, (unsigned)ty);
        p += 4;
        for (int y = y0; y < y1; y++) {
            const uint8_t *s = frame + (size_t)y * stride + (size_t)x0 * 4;
            uint8_t *row = p;
            for (int x = 0; x < x1 - x0; x++, s += 4, p += 3) {
                p[0] = s[2];
                p[1] = s[1];
                p[2] = s[0];
            }
            for (int i = (x1 - x0) * 3 - 1; i >= 3; i--)
                row[i] = (uint8_t)(row[i] - row[i - 3]);
        }
        n++;
        idx++;
    }
    int next = idx;
    while (next < total && !t->dirty[next])
        next++;
    int last = next >= total;

    size_t raw_len = (size_t)(p - t->raw);
    uint8_t *hdr = t->out + TILE_FRAME_HEADROOM;
    unsigned flags = 0;
    if (full) {
        if (*cursor == 0)
            flags |= TILE_FLAG_FULL_BEGIN;
        if (last)
            flags |= TILE_FLAG_FULL_END;
    }
    hdr[0] = 1;
    hdr[1] = (uint8_t)flags;
    put16(hdr + 2, (unsigned)t->w);
    put16(hdr + 4, (unsigned)t->h);
    put16(hdr + 6, TILE_SIZE);
    put16(hdr + 8, (unsigned)n);
    put32(hdr + 10, t->frame_id++);
    put32(hdr + 14, (uint32_t)raw_len);

    deflateReset(&t->zs);
    t->zs.next_in = t->raw;
    t->zs.avail_in = (uInt)raw_len;
    t->zs.next_out = hdr + TILE_HEADER_LEN;
    t->zs.avail_out = (uInt)(t->out_cap - TILE_FRAME_HEADROOM - TILE_HEADER_LEN);
    if (deflate(&t->zs, Z_FINISH) != Z_STREAM_END)
        return -1;
    *payload = hdr;
    *len = TILE_HEADER_LEN + t->zs.total_out;
    *cursor = next;
    if (last) {
        memset(t->dirty, 0, (size_t)total);
        t->ndirty = 0;
    }
    return 1;
}

int tile_apply_packet(uint8_t *rgb, int w, int h, const uint8_t *payload, size_t len, int *flags)
{
    if (len < TILE_HEADER_LEN || payload[0] != 1)
        return -1;
    int pw = payload[2] | payload[3] << 8, ph = payload[4] | payload[5] << 8;
    int ts = payload[6] | payload[7] << 8, n = payload[8] | payload[9] << 8;
    uint32_t raw_len = (uint32_t)payload[14] | (uint32_t)payload[15] << 8 | (uint32_t)payload[16] << 16 |
                       (uint32_t)payload[17] << 24;
    if (pw != w || ph != h || ts != TILE_SIZE)
        return -1;
    if (flags)
        *flags = payload[1];
    uint8_t *raw = malloc(raw_len ? raw_len : 1);
    if (!raw)
        return -1;
    uLongf out = raw_len;
    if (uncompress(raw, &out, payload + TILE_HEADER_LEN, (uLong)(len - TILE_HEADER_LEN)) != Z_OK || out != raw_len) {
        free(raw);
        return -1;
    }
    const uint8_t *p = raw, *end = raw + raw_len;
    for (int i = 0; i < n; i++) {
        if (end - p < 4) {
            free(raw);
            return -1;
        }
        int tx = p[0] | p[1] << 8, ty = p[2] | p[3] << 8;
        p += 4;
        int x0 = tx * ts, y0 = ty * ts;
        int x1 = x0 + ts > w ? w : x0 + ts, y1 = y0 + ts > h ? h : y0 + ts;
        if (x0 >= w || y0 >= h || end - p < (long)(x1 - x0) * 3 * (y1 - y0)) {
            free(raw);
            return -1;
        }
        for (int y = y0; y < y1; y++) {
            uint8_t *d = rgb + ((size_t)y * w + x0) * 3;
            int rb = (x1 - x0) * 3;
            memcpy(d, p, (size_t)rb);
            for (int k = 3; k < rb; k++)
                d[k] = (uint8_t)(d[k] + d[k - 3]);
            p += rb;
        }
    }
    free(raw);
    return 0;
}

static int roundtrip(tile_state *t, const uint8_t *frame, int w, int h, uint8_t *rgb, int full, int expect_packets_min)
{
    int cursor = 0, packets = 0;
    uint8_t *pl;
    size_t len;
    int r;
    int begin = 0, end = 0;
    while ((r = tile_next_packet(t, frame, w * 4, full, &cursor, &pl, &len)) == 1) {
        int fl = 0;
        if (tile_apply_packet(rgb, w, h, pl, len, &fl) != 0)
            return -1;
        begin += fl & TILE_FLAG_FULL_BEGIN ? 1 : 0;
        end += fl & TILE_FLAG_FULL_END ? 1 : 0;
        packets++;
    }
    if (r < 0 || packets < expect_packets_min)
        return -1;
    if (full && (begin != 1 || end != 1))
        return -1;
    return 0;
}

static int same(const uint8_t *frame, const uint8_t *rgb, int w, int h)
{
    for (size_t i = 0; i < (size_t)w * h; i++) {
        if (frame[i * 4] != rgb[i * 3 + 2] || frame[i * 4 + 1] != rgb[i * 3 + 1] || frame[i * 4 + 2] != rgb[i * 3])
            return 0;
    }
    return 1;
}

int tile_selftest(const uint8_t *frame, int w, int h)
{
    tile_state *t = tile_open(w, h);
    uint8_t *rgb = calloc((size_t)w * h, 3);
    uint8_t *mod = malloc((size_t)w * h * 4);
    int rc = -1;
    if (!t || !rgb || !mod)
        goto out;
    memcpy(mod, frame, (size_t)w * h * 4);

    rect_t all = {0, 0, w, h};
    tile_diff(t, frame, w * 4, all);
    tile_dirty_all(t);
    if (roundtrip(t, frame, w, h, rgb, 1, 1) != 0 || !same(frame, rgb, w, h)) {
        LOG("selftest: full refresh mismatch");
        goto out;
    }

    int n_expected = 0;
    int pts[3][2] = {{0, 0}, {w - 1, h - 1}, {w / 2, h / 3}};
    int seen[3] = {-1, -1, -1};
    for (int i = 0; i < 3; i++) {
        size_t o = ((size_t)pts[i][1] * w + pts[i][0]) * 4;
        mod[o] = (uint8_t)(mod[o] ^ 0x5a);
        int tile = (pts[i][1] / TILE_SIZE) * ((w + TILE_SIZE - 1) / TILE_SIZE) + pts[i][0] / TILE_SIZE;
        int dup = 0;
        for (int k = 0; k < i; k++)
            dup |= seen[k] == tile;
        seen[i] = tile;
        if (!dup)
            n_expected++;
    }
    int n = tile_diff(t, mod, w * 4, all);
    if (n != n_expected) {
        LOG("selftest: expected %d dirty tiles, got %d", n_expected, n);
        goto out;
    }
    if (roundtrip(t, mod, w, h, rgb, 0, 1) != 0 || !same(mod, rgb, w, h)) {
        LOG("selftest: delta mismatch");
        goto out;
    }
    if (tile_diff(t, mod, w * 4, all) != 0) {
        LOG("selftest: unchanged frame reported dirty tiles");
        goto out;
    }
    rc = 0;
out:
    tile_close(t);
    free(rgb);
    free(mod);
    return rc;
}
