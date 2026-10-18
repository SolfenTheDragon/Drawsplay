#ifndef DRAWSPLAY_X11_CAPTURE_H
#define DRAWSPLAY_X11_CAPTURE_H

#include "common.h"

typedef struct x11cap x11cap;

/* w/h of 0 select the whole root window; the size is rounded down to even. */
x11cap *x11_open(const char *display, int x, int y, int w, int h, int use_damage, int use_cursor);
void x11_close(x11cap *c);

int x11_fd(const x11cap *c);
int x11_width(const x11cap *c);
int x11_height(const x11cap *c);

/* Composited BGRA frame, stride width*4, persistent across captures. */
const uint8_t *x11_frame(const x11cap *c);

/* Processes queued X events and accumulates damage. Returns 1 if a capture is needed. */
int x11_drain(x11cap *c);
int x11_pending(const x11cap *c);
void x11_poll_pointer(x11cap *c);
void x11_mark_full(x11cap *c);

/* Grabs the accumulated damage into the frame; dirty receives the changed rectangle (even aligned). */
int x11_capture(x11cap *c, rect_t *dirty);

#endif
