#include "x11_capture.h"

#include <stdlib.h>
#include <string.h>
#include <sys/ipc.h>
#include <sys/shm.h>

#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/extensions/XShm.h>
#include <X11/extensions/Xdamage.h>
#include <X11/extensions/Xfixes.h>

struct x11cap {
    Display *dpy;
    Window root;
    int rx, ry, w, h;
    int use_damage, use_cursor;

    XImage *img;
    XShmSegmentInfo shm;

    uint8_t *frame;
    uint8_t *under;
    size_t under_cap;

    Damage dmg;
    int dmg_ev, fixes_ev;
    rect_t gdirty;
    int cursor_dirty;

    uint32_t *cpix;
    int cw, ch, cxhot, cyhot;
    int px, py;
    rect_t cur_rect;
};

static int x_error_handler(Display *dpy, XErrorEvent *ev)
{
    char msg[128];
    XGetErrorText(dpy, ev->error_code, msg, sizeof msg);
    LOG("X error: %s (request %d.%d)", msg, ev->request_code, ev->minor_code);
    exit(1);
}

static void fetch_cursor(x11cap *c)
{
    XFixesCursorImage *ci = XFixesGetCursorImage(c->dpy);
    if (!ci)
        return;
    size_t n = (size_t)ci->width * ci->height;
    uint32_t *p = n ? malloc(n * sizeof *p) : NULL;
    if (p || n == 0) {
        for (size_t i = 0; i < n; i++)
            p[i] = (uint32_t)ci->pixels[i];
        free(c->cpix);
        c->cpix = p;
        c->cw = ci->width;
        c->ch = ci->height;
        c->cxhot = ci->xhot;
        c->cyhot = ci->yhot;
    }
    XFree(ci);
}

void x11_mark_full(x11cap *c)
{
    c->gdirty.x = c->gdirty.y = 0;
    c->gdirty.w = c->w;
    c->gdirty.h = c->h;
}

x11cap *x11_open(const char *display, int x, int y, int w, int h, int use_damage, int use_cursor)
{
    x11cap *c = calloc(1, sizeof *c);
    if (!c)
        return NULL;
    c->use_damage = use_damage;
    c->use_cursor = use_cursor;
    XSetErrorHandler(x_error_handler);
    c->dpy = XOpenDisplay(display);
    if (!c->dpy) {
        LOG("cannot open X display %s", display ? display : "(DISPLAY)");
        goto fail;
    }
    c->root = DefaultRootWindow(c->dpy);
    int sw = DisplayWidth(c->dpy, DefaultScreen(c->dpy));
    int sh = DisplayHeight(c->dpy, DefaultScreen(c->dpy));
    if (w <= 0)
        w = sw - x;
    if (h <= 0)
        h = sh - y;
    w &= ~1;
    h &= ~1;
    if (x < 0 || y < 0 || w < 16 || h < 16 || x + w > sw || y + h > sh) {
        LOG("capture region %d,%d %dx%d is invalid for the %dx%d screen", x, y, w, h, sw, sh);
        goto fail;
    }
    c->rx = x;
    c->ry = y;
    c->w = w;
    c->h = h;

    int major, minor, err;
    Bool pixmaps;
    if (!XShmQueryExtension(c->dpy) || !XShmQueryVersion(c->dpy, &major, &minor, &pixmaps)) {
        LOG("X server lacks the MIT-SHM extension");
        goto fail;
    }
    if (use_damage) {
        if (!XDamageQueryExtension(c->dpy, &c->dmg_ev, &err)) {
            LOG("X server lacks the DAMAGE extension");
            goto fail;
        }
    }
    if (use_cursor || use_damage) {
        if (!XFixesQueryExtension(c->dpy, &c->fixes_ev, &err)) {
            if (use_cursor) {
                LOG("X server lacks the XFIXES extension (needed for cursor capture; use --no-cursor)");
                goto fail;
            }
        }
    }

    c->img = XShmCreateImage(c->dpy, DefaultVisual(c->dpy, DefaultScreen(c->dpy)),
                             DefaultDepth(c->dpy, DefaultScreen(c->dpy)), ZPixmap, NULL, &c->shm, w, h);
    if (!c->img) {
        LOG("XShmCreateImage failed");
        goto fail;
    }
    if (c->img->bits_per_pixel != 32 || c->img->red_mask != 0xff0000 || c->img->green_mask != 0xff00 ||
        c->img->blue_mask != 0xff) {
        LOG("unsupported X visual (bpp %d, masks %lx/%lx/%lx); a 32-bit BGRA root is required", c->img->bits_per_pixel,
            c->img->red_mask, c->img->green_mask, c->img->blue_mask);
        goto fail;
    }
    c->shm.shmid = shmget(IPC_PRIVATE, (size_t)c->img->bytes_per_line * h, IPC_CREAT | 0600);
    if (c->shm.shmid < 0) {
        LOG("shmget failed");
        goto fail;
    }
    c->shm.shmaddr = c->img->data = shmat(c->shm.shmid, NULL, 0);
    c->shm.readOnly = False;
    shmctl(c->shm.shmid, IPC_RMID, NULL);
    if (c->shm.shmaddr == (char *)-1) {
        c->shm.shmaddr = c->img->data = NULL;
        LOG("shmat failed");
        goto fail;
    }
    if (!XShmAttach(c->dpy, &c->shm)) {
        LOG("XShmAttach failed");
        goto fail;
    }
    XSync(c->dpy, False);

    c->frame = calloc((size_t)w * h, 4);
    if (!c->frame) {
        LOG("out of memory");
        goto fail;
    }

    if (use_damage) {
        c->dmg = XDamageCreate(c->dpy, c->root, XDamageReportRawRectangles);
        XSync(c->dpy, False);
    }
    if (use_cursor) {
        XFixesSelectCursorInput(c->dpy, c->root, XFixesDisplayCursorNotifyMask);
        fetch_cursor(c);
        c->cursor_dirty = 1;
        c->px = c->py = -(1 << 20);
    }
    x11_mark_full(c);
    return c;

fail:
    x11_close(c);
    return NULL;
}

void x11_close(x11cap *c)
{
    if (!c)
        return;
    if (c->dpy) {
        if (c->shm.shmaddr)
            XShmDetach(c->dpy, &c->shm);
        if (c->dmg)
            XDamageDestroy(c->dpy, c->dmg);
    }
    if (c->img) {
        c->img->data = NULL;
        XDestroyImage(c->img);
    }
    if (c->shm.shmaddr && c->shm.shmaddr != (char *)-1)
        shmdt(c->shm.shmaddr);
    free(c->under);
    free(c->frame);
    free(c->cpix);
    if (c->dpy)
        XCloseDisplay(c->dpy);
    free(c);
}

int x11_fd(const x11cap *c)
{
    return ConnectionNumber(c->dpy);
}

int x11_width(const x11cap *c)
{
    return c->w;
}

int x11_height(const x11cap *c)
{
    return c->h;
}

const uint8_t *x11_frame(const x11cap *c)
{
    return c->frame;
}

int x11_pending(const x11cap *c)
{
    return !rect_empty(&c->gdirty) || c->cursor_dirty;
}

static void handle_events(x11cap *c)
{
    while (XPending(c->dpy)) {
        XEvent ev;
        XNextEvent(c->dpy, &ev);
        if (c->use_damage && ev.type == c->dmg_ev + XDamageNotify) {
            XDamageNotifyEvent *d = (XDamageNotifyEvent *)&ev;
            rect_t r = {d->area.x - c->rx, d->area.y - c->ry, d->area.width, d->area.height};
            r = rect_clip(r, c->w, c->h);
            c->gdirty = rect_union(c->gdirty, r);
        } else if (c->use_cursor && ev.type == c->fixes_ev + XFixesCursorNotify) {
            fetch_cursor(c);
            c->cursor_dirty = 1;
        }
    }
}

int x11_drain(x11cap *c)
{
    handle_events(c);
    return x11_pending(c);
}

void x11_poll_pointer(x11cap *c)
{
    if (!c->use_cursor)
        return;
    Window rw, cw;
    int rxp, ryp, wx, wy;
    unsigned int mask;
    if (!XQueryPointer(c->dpy, c->root, &rw, &cw, &rxp, &ryp, &wx, &wy, &mask))
        return;
    if (rxp != c->px || ryp != c->py) {
        c->px = rxp;
        c->py = ryp;
        c->cursor_dirty = 1;
    }
}

static void blend_cursor(x11cap *c, rect_t cr, int ox, int oy)
{
    for (int y = cr.y; y < cr.y + cr.h; y++) {
        uint8_t *d = c->frame + ((size_t)y * c->w + cr.x) * 4;
        const uint32_t *s = c->cpix + (size_t)(y - oy) * c->cw + (cr.x - ox);
        for (int x = 0; x < cr.w; x++, d += 4) {
            uint32_t p = s[x];
            uint32_t a = p >> 24;
            if (a == 0)
                continue;
            uint32_t inv = 255 - a;
            d[0] = (uint8_t)((p & 0xff) + (d[0] * inv + 127) / 255);
            d[1] = (uint8_t)(((p >> 8) & 0xff) + (d[1] * inv + 127) / 255);
            d[2] = (uint8_t)(((p >> 16) & 0xff) + (d[2] * inv + 127) / 255);
        }
    }
}

static void restore_under(x11cap *c)
{
    rect_t r = c->cur_rect;
    for (int y = 0; y < r.h; y++)
        memcpy(c->frame + ((size_t)(r.y + y) * c->w + r.x) * 4, c->under + (size_t)y * r.w * 4, (size_t)r.w * 4);
}

static int save_under(x11cap *c, rect_t r)
{
    size_t need = (size_t)r.w * r.h * 4;
    if (need > c->under_cap) {
        uint8_t *p = realloc(c->under, need);
        if (!p)
            return -1;
        c->under = p;
        c->under_cap = need;
    }
    for (int y = 0; y < r.h; y++)
        memcpy(c->under + (size_t)y * r.w * 4, c->frame + ((size_t)(r.y + y) * c->w + r.x) * 4, (size_t)r.w * 4);
    return 0;
}

int x11_capture(x11cap *c, rect_t *dirty)
{
    if (c->use_damage) {
        XDamageSubtract(c->dpy, c->dmg, None, None);
        XSync(c->dpy, False);
    }
    handle_events(c);
    x11_poll_pointer(c);

    rect_t g = c->gdirty;
    c->gdirty.x = c->gdirty.y = c->gdirty.w = c->gdirty.h = 0;
    c->cursor_dirty = 0;

    rect_t old_cur = c->cur_rect;
    if (c->use_cursor && !rect_empty(&old_cur))
        restore_under(c);

    if (!rect_empty(&g)) {
        c->img->width = g.w;
        c->img->height = g.h;
        c->img->bytes_per_line = g.w * 4;
        Status ok = XShmGetImage(c->dpy, c->root, c->img, c->rx + g.x, c->ry + g.y, AllPlanes);
        c->img->width = c->w;
        c->img->height = c->h;
        c->img->bytes_per_line = c->w * 4;
        if (!ok) {
            LOG("XShmGetImage failed for %d,%d %dx%d", c->rx + g.x, c->ry + g.y, g.w, g.h);
            return -1;
        }
        for (int y = 0; y < g.h; y++)
            memcpy(c->frame + ((size_t)(g.y + y) * c->w + g.x) * 4, c->img->data + (size_t)y * g.w * 4,
                   (size_t)g.w * 4);
    }

    rect_t d = g;
    if (c->use_cursor) {
        rect_t cr = {0, 0, 0, 0};
        int ox = c->px - c->rx - c->cxhot, oy = c->py - c->ry - c->cyhot;
        if (c->cpix) {
            rect_t raw = {ox, oy, c->cw, c->ch};
            cr = rect_clip(raw, c->w, c->h);
        }
        if (!rect_empty(&cr)) {
            if (save_under(c, cr) != 0) {
                LOG("out of memory");
                return -1;
            }
            blend_cursor(c, cr, ox, oy);
        }
        c->cur_rect = cr;
        d = rect_union(d, old_cur);
        d = rect_union(d, cr);
    }

    if (!rect_empty(&d)) {
        int x0 = d.x & ~1, y0 = d.y & ~1;
        int x1 = (d.x + d.w + 1) & ~1, y1 = (d.y + d.h + 1) & ~1;
        if (x1 > c->w)
            x1 = c->w;
        if (y1 > c->h)
            y1 = c->h;
        d.x = x0;
        d.y = y0;
        d.w = x1 - x0;
        d.h = y1 - y0;
    }
    *dirty = d;
    return 0;
}
