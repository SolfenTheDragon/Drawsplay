#ifndef DRAWSPLAY_COMMON_H
#define DRAWSPLAY_COMMON_H

#include <stdint.h>
#include <stdio.h>
#include <time.h>

typedef struct {
    int x, y, w, h;
} rect_t;

#define LOG(fmt, ...) fprintf(stderr, "drawsplay-media: " fmt "\n", ##__VA_ARGS__)

static inline uint64_t now_us(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000u + (uint64_t)ts.tv_nsec / 1000u;
}

static inline int rect_empty(const rect_t *r)
{
    return r->w <= 0 || r->h <= 0;
}

static inline rect_t rect_union(rect_t a, rect_t b)
{
    if (rect_empty(&a))
        return b;
    if (rect_empty(&b))
        return a;
    int x0 = a.x < b.x ? a.x : b.x;
    int y0 = a.y < b.y ? a.y : b.y;
    int x1 = a.x + a.w > b.x + b.w ? a.x + a.w : b.x + b.w;
    int y1 = a.y + a.h > b.y + b.h ? a.y + a.h : b.y + b.h;
    rect_t r = {x0, y0, x1 - x0, y1 - y0};
    return r;
}

static inline rect_t rect_clip(rect_t r, int w, int h)
{
    int x0 = r.x < 0 ? 0 : r.x;
    int y0 = r.y < 0 ? 0 : r.y;
    int x1 = r.x + r.w > w ? w : r.x + r.w;
    int y1 = r.y + r.h > h ? h : r.y + r.h;
    rect_t o = {x0, y0, x1 - x0, y1 - y0};
    if (o.w <= 0 || o.h <= 0) {
        o.x = o.y = o.w = o.h = 0;
    }
    return o;
}

#endif
