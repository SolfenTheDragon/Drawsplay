#define _GNU_SOURCE
#include <errno.h>
#include <poll.h>
#include <pthread.h>
#include <signal.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include <sys/eventfd.h>
#include <unistd.h>

#include "common.h"
#include "tile.h"
#include "va_encode.h"
#include "x11_capture.h"

/* 2: --max-inflight and the "A <n>" frame-ack command.
 * 3: --cursor overlay (cursor sent as KIND_CURSOR frames, not in the video).
 * The host passes these only to helpers reporting at least that version. */
#define HELPER_VERSION 3
#define KIND_AU 1
#define KIND_TILE 2
#define KIND_MODE 3
#define KIND_CURSOR 4

#define POINTER_POLL_US 8000

enum { POLICY_AUTO, POLICY_VIDEO, POLICY_TILE };

typedef struct {
    int probe, selftest;
    const char *display, *render_node;
    int x, y, w, h, fps, kbps, gop;
    int tiles_policy;
    int damage, cursor, cursor_overlay;
    int keepalive_ms;
    int max_inflight;
} options;

static struct {
    atomic_uint kick_seq;
    atomic_llong acked;
    atomic_int acks_seen;
    atomic_int force_idr;
    atomic_int new_kbps;
    atomic_int mode_req;
    atomic_int quit;
    int wake_fd;
} g_cmd;

static void wake(void)
{
    uint64_t one = 1;
    if (write(g_cmd.wake_fd, &one, sizeof one) < 0 && errno != EAGAIN) {
        /* wake-ups are best effort; the loop re-checks state on its next timeout */
    }
}

static void parse_line(char *line)
{
    while (*line == ' ')
        line++;
    switch (line[0]) {
    case 'K':
        atomic_store(&g_cmd.kick_seq, (unsigned)strtoul(line + 1, NULL, 10));
        break;
    case 'A': {
        /* Cumulative count of this helper's frames the tablet finished. */
        long long v = strtoll(line + 1, NULL, 10);
        long long cur = atomic_load(&g_cmd.acked);
        while (v > cur && !atomic_compare_exchange_weak(&g_cmd.acked, &cur, v)) {
        }
        atomic_store(&g_cmd.acks_seen, 1);
        break;
    }
    case 'I':
        atomic_store(&g_cmd.force_idr, 1);
        break;
    case 'B': {
        long v = strtol(line + 1, NULL, 10);
        if (v > 0)
            atomic_store(&g_cmd.new_kbps, (int)v);
        break;
    }
    case 'M': {
        char *a = line + 1;
        while (*a == ' ')
            a++;
        if (!strncmp(a, "video", 5))
            atomic_store(&g_cmd.mode_req, 1 + POLICY_VIDEO);
        else if (!strncmp(a, "tile", 4))
            atomic_store(&g_cmd.mode_req, 1 + POLICY_TILE);
        else if (!strncmp(a, "auto", 4))
            atomic_store(&g_cmd.mode_req, 1 + POLICY_AUTO);
        break;
    }
    case 'Q':
        atomic_store(&g_cmd.quit, 1);
        break;
    default:
        return;
    }
    wake();
}

static void *stdin_thread(void *arg)
{
    (void)arg;
    char buf[512];
    size_t n = 0;
    struct pollfd pfd = {0, POLLIN, 0};
    for (;;) {
        int pr = poll(&pfd, 1, -1);
        if (pr < 0) {
            if (errno == EINTR)
                continue;
            break;
        }
        ssize_t r = read(0, buf + n, sizeof buf - n - 1);
        if (r < 0 && errno == EINTR)
            continue;
        if (r <= 0)
            break;
        n += (size_t)r;
        char *start = buf, *nl;
        while ((nl = memchr(start, '\n', n - (size_t)(start - buf)))) {
            *nl = 0;
            parse_line(start);
            start = nl + 1;
        }
        n -= (size_t)(start - buf);
        memmove(buf, start, n);
        if (n >= sizeof buf - 1)
            n = 0;
    }
    atomic_store(&g_cmd.quit, 1);
    wake();
    return NULL;
}

static void write_all(const uint8_t *p, size_t n)
{
    while (n) {
        ssize_t w = write(1, p, n);
        if (w < 0) {
            if (errno == EINTR)
                continue;
            LOG("stdout write failed: %s", strerror(errno));
            exit(0);
        }
        p += w;
        n -= (size_t)w;
    }
}

static void put32(uint8_t *p, uint32_t v)
{
    p[0] = (uint8_t)v;
    p[1] = (uint8_t)(v >> 8);
    p[2] = (uint8_t)(v >> 16);
    p[3] = (uint8_t)(v >> 24);
}

/* payload must have 16 writable bytes in front of it. */
static void send_frame(uint8_t *payload, size_t len, int kind, int flags, uint32_t seq, uint64_t t0)
{
    uint8_t *h = payload - 16;
    uint64_t pipe = now_us() - t0;
    put32(h, (uint32_t)len);
    h[4] = (uint8_t)kind;
    h[5] = (uint8_t)flags;
    h[6] = h[7] = 0;
    put32(h + 8, seq);
    put32(h + 12, pipe > 0xffffffffu ? 0xffffffffu : (uint32_t)pipe);
    write_all(h, len + 16);
}

static void send_mode(int tile)
{
    uint8_t buf[17];
    put32(buf, 1);
    buf[4] = KIND_MODE;
    buf[5] = buf[6] = buf[7] = 0;
    put32(buf + 8, 0);
    put32(buf + 12, 0);
    buf[16] = (uint8_t)tile;
    write_all(buf, sizeof buf);
}

static void on_alarm(int sig)
{
    (void)sig;
    static const char msg[] = "drawsplay-media: initialisation timed out\n";
    if (write(2, msg, sizeof msg - 1)) {
    }
    _exit(2);
}

static int usage(void)
{
    fprintf(stderr,
            "usage: drawsplay-media --probe [--render-node PATH]\n"
            "       drawsplay-media --display :0 [--x N --y N --w N --h N] --fps N --bitrate-kbps N\n"
            "                       [--gop N] [--render-node PATH] [--tiles auto|off|force]\n"
            "                       [--damage on|off] [--keepalive-ms N] [--no-cursor]\n"
            "                       [--max-inflight N] [--cursor overlay|off]\n"
            "       drawsplay-media --selftest-tiles --display :0\n");
    return 2;
}

static int parse_options(int argc, char **argv, options *o)
{
    memset(o, 0, sizeof *o);
    o->display = NULL;
    o->fps = 60;
    o->kbps = 8000;
    o->tiles_policy = POLICY_AUTO;
    o->damage = 1;
    o->cursor = 1;
    o->keepalive_ms = 1000;
    for (int i = 1; i < argc; i++) {
        const char *a = argv[i];
        const char *v = i + 1 < argc ? argv[i + 1] : NULL;
        if (!strcmp(a, "--probe")) {
            o->probe = 1;
        } else if (!strcmp(a, "--selftest-tiles")) {
            o->selftest = 1;
        } else if (!strcmp(a, "--no-cursor")) {
            o->cursor = 0;
        } else if (!v) {
            LOG("option %s needs a value", a);
            return -1;
        } else if (!strcmp(a, "--display")) {
            o->display = v;
            i++;
        } else if (!strcmp(a, "--render-node")) {
            o->render_node = v;
            i++;
        } else if (!strcmp(a, "--x")) {
            o->x = atoi(v);
            i++;
        } else if (!strcmp(a, "--y")) {
            o->y = atoi(v);
            i++;
        } else if (!strcmp(a, "--w")) {
            o->w = atoi(v);
            i++;
        } else if (!strcmp(a, "--h")) {
            o->h = atoi(v);
            i++;
        } else if (!strcmp(a, "--fps")) {
            o->fps = atoi(v);
            i++;
        } else if (!strcmp(a, "--bitrate-kbps")) {
            o->kbps = atoi(v);
            i++;
        } else if (!strcmp(a, "--gop")) {
            o->gop = atoi(v);
            i++;
        } else if (!strcmp(a, "--cursor")) {
            if (!strcmp(v, "overlay")) {
                o->cursor = 1;
                o->cursor_overlay = 1;
            } else if (!strcmp(v, "off")) {
                o->cursor = 0;
                o->cursor_overlay = 0;
            } else {
                LOG("--cursor takes overlay or off");
                return -1;
            }
            i++;
        } else if (!strcmp(a, "--keepalive-ms")) {
            o->keepalive_ms = atoi(v);
            i++;
        } else if (!strcmp(a, "--max-inflight")) {
            o->max_inflight = atoi(v);
            i++;
        } else if (!strcmp(a, "--tiles")) {
            if (!strcmp(v, "auto"))
                o->tiles_policy = POLICY_AUTO;
            else if (!strcmp(v, "off"))
                o->tiles_policy = POLICY_VIDEO;
            else if (!strcmp(v, "force"))
                o->tiles_policy = POLICY_TILE;
            else {
                LOG("bad --tiles value %s", v);
                return -1;
            }
            i++;
        } else if (!strcmp(a, "--damage")) {
            if (!strcmp(v, "on"))
                o->damage = 1;
            else if (!strcmp(v, "off"))
                o->damage = 0;
            else {
                LOG("bad --damage value %s", v);
                return -1;
            }
            i++;
        } else {
            LOG("unknown option %s", a);
            return -1;
        }
    }
    if (o->fps < 1 || o->fps > 240 || o->kbps < 100 || o->keepalive_ms < 0 || o->max_inflight < 0) {
        LOG("invalid --fps/--bitrate-kbps/--keepalive-ms/--max-inflight");
        return -1;
    }
    if (o->gop <= 0)
        o->gop = 2 * o->fps;
    return 0;
}

static int run_probe(const options *o)
{
    alarm(5);
    va_info info;
    memset(&info, 0, sizeof info);
    va_enc *e = va_enc_open(o->render_node, 1280, 720, 30, 4000, 60, &info);
    const char *reason = "no usable VA-API H.264 encoder";
    if (!e)
        goto fail;
    size_t n = (size_t)1280 * 720 * 4;
    uint8_t *img = malloc(n);
    if (!img) {
        reason = "out of memory";
        va_enc_close(e);
        goto fail;
    }
    for (int y = 0; y < 720; y++) {
        for (int x = 0; x < 1280; x++) {
            uint8_t *p = img + ((size_t)y * 1280 + x) * 4;
            p[0] = (uint8_t)(x ^ y);
            p[1] = (uint8_t)(x / 5);
            p[2] = (uint8_t)(y / 3);
            p[3] = 0xff;
        }
    }
    rect_t all = {0, 0, 1280, 720};
    va_enc_convert(e, img, 1280 * 4, all);
    free(img);
    uint8_t *au;
    size_t len;
    int idr;
    int rc = va_enc_encode(e, 1, &au, &len, &idr);
    uint32_t m = rc == 0 ? va_nal_mask(au, len) : 0;
    int good = rc == 0 && (m & (1u << 7)) && (m & (1u << 8)) && (m & (1u << 5));
    if (good) {
        rc = va_enc_encode(e, 0, &au, &len, &idr);
        good = rc == 0 && (va_nal_mask(au, len) & (1u << 1));
    }
    va_enc_close(e);
    if (!good) {
        reason = "test encode did not produce SPS/PPS/IDR followed by a P frame";
        goto fail;
    }
    printf("{\"ok\":true,\"vendor\":\"%s\",\"entrypoint\":\"%s\",\"profile\":\"%s\",\"helperVersion\":%d}\n",
           info.vendor, info.entrypoint, info.profile, HELPER_VERSION);
    fflush(stdout);
    return 0;
fail:
    printf("{\"ok\":false,\"reason\":\"%s\"}\n", reason);
    fflush(stdout);
    return 1;
}

static int run_selftest(const options *o)
{
    alarm(20);
    x11cap *c = x11_open(o->display, o->x, o->y, o->w, o->h, 0, o->cursor);
    if (!c)
        return 1;
    rect_t d;
    if (x11_capture(c, &d) != 0) {
        x11_close(c);
        return 1;
    }
    int rc = tile_selftest(x11_frame(c), x11_width(c), x11_height(c));
    LOG("tile selftest %s (%dx%d)", rc == 0 ? "passed" : "FAILED", x11_width(c), x11_height(c));
    x11_close(c);
    return rc == 0 ? 0 : 1;
}

typedef struct {
    options o;
    x11cap *cap;
    va_enc *enc;
    tile_state *tiles;
    int w, h, mode;
    int policy;
    int low_run, high_run, any_changed;
    int video_ready;
} session;

static void emit_tile_packets(session *s, int full, uint32_t seq, uint64_t t0)
{
    int cursor = 0;
    uint8_t *pl;
    size_t len;
    int r;
    while ((r = tile_next_packet(s->tiles, x11_frame(s->cap), s->w * 4, full, &cursor, &pl, &len)) == 1) {
        int flags = pl[1];
        send_frame(pl, len, KIND_TILE, flags, seq, t0);
    }
    if (r < 0) {
        LOG("tile deflate failed");
        exit(1);
    }
}

/*
 * Frame window (flow control borrowed from SuperDisplay; see
 * wiki/topics/superdisplay-windows-pipeline.md). With --max-inflight N and
 * once the host forwards tablet acks ("A <n>"), a new video frame is
 * captured and encoded only while fewer than N encoded frames are
 * unacknowledged. A deferred capture loses nothing: damage keeps
 * accumulating, and the next capture after an ack takes the newest screen,
 * so a slow link or decoder costs frame rate, never queued latency.
 */
/* Short: a full window without ack progress this long means a lost ack, and
 * the wait is a visible freeze (normal round trips are milliseconds). */
#define ACK_STALL_US 300000
/* The window covers one measured send->ack round trip (minimum over a
 * sliding 2-4 s span), at least 2 frames and at most --max-inflight. */
#define MIN_WINDOW 2
#define RTT_SPAN_US 2000000
#define SEND_RING 512

static struct {
    long long sent, assumed_through, last_acked, stalls;
    uint64_t full_since; /* when the window last became full; 0 = not full */
    uint64_t send_us[SEND_RING];
    int64_t rtt_best, rtt_next;
    uint64_t rtt_roll;
    int window;
} g_win = {.rtt_best = INT64_MAX, .rtt_next = INT64_MAX};

static void rtt_add(uint64_t now, int64_t v)
{
    if (now >= g_win.rtt_roll) {
        g_win.rtt_best = g_win.rtt_next;
        g_win.rtt_next = INT64_MAX;
        g_win.rtt_roll = now + RTT_SPAN_US;
    }
    if (v < g_win.rtt_next)
        g_win.rtt_next = v;
    if (v < g_win.rtt_best)
        g_win.rtt_best = v;
}

static int window_for_rtt(int64_t rtt, int fps, int max)
{
    if (rtt == INT64_MAX)
        return max;
    int64_t interval = 1000000 / (fps > 0 ? fps : 60);
    int64_t frames = (rtt + interval - 1) / interval + 1;
    if (frames < MIN_WINDOW)
        frames = MIN_WINDOW;
    if (frames > max)
        frames = max;
    return (int)frames;
}

static int window_open(const options *o)
{
    if (o->max_inflight <= 0 || !atomic_load(&g_cmd.acks_seen))
        return 1;
    if (!g_win.window)
        g_win.window = o->max_inflight;
    long long acked = atomic_load(&g_cmd.acked);
    uint64_t now = now_us();
    if (acked != g_win.last_acked) {
        /* Acks count this helper's frames in order: frame acked-1 is the
         * newest acknowledged one. */
        long long frame = acked - 1;
        if (acked > g_win.last_acked && frame >= 0 && frame < g_win.sent && g_win.sent - frame <= SEND_RING) {
            rtt_add(now, (int64_t)(now - g_win.send_us[frame % SEND_RING]));
            g_win.window = window_for_rtt(g_win.rtt_best, o->fps, o->max_inflight);
        }
        g_win.last_acked = acked;
        g_win.full_since = 0; /* progress: restart the stall clock */
    }
    /* Frames before assumed_through were written off by the stall guard;
     * late acks for them only catch up to this floor, so they never widen
     * the window. */
    long long done = acked > g_win.assumed_through ? acked : g_win.assumed_through;
    long long inflight = g_win.sent - done;
    if (inflight < g_win.window) {
        g_win.full_since = 0;
        return 1;
    }
    /* The stall clock runs only while the window is full, so an idle pause
     * never makes the first full window of the next burst look stalled. */
    if (!g_win.full_since) {
        g_win.full_since = now;
    } else if (now - g_win.full_since > ACK_STALL_US) {
        /* Acks stopped (lost on a reconnect, or a client that quit acking):
         * write the in-flight frames off instead of freezing the stream. */
        g_win.assumed_through = g_win.sent;
        g_win.full_since = 0;
        g_win.stalls++;
        LOG("frame acks stalled; releasing %lld in-flight frames", inflight);
        return 1;
    }
    return 0;
}

/*
 * Cursor overlay (--cursor overlay): the cursor is not blended into the
 * frames. Changes go out as small KIND_CURSOR frames at once, independent of
 * video frames, for the tablet to draw. Payload, big-endian (the host
 * forwards it as video packet 5; see CursorPacket.kt): u8 flags (1 visible,
 * 2 shape follows), i32 x, i32 y (top-left of the cursor image in frame
 * pixels), u32 shape id; with a shape: u16 w, u16 h, u16 hot x, u16 hot y,
 * w*h*4 bytes BGRA, straight alpha.
 */
#define CURSOR_MAX_SIDE 256 /* larger shapes are rejected by the tablet */
#define CURSOR_FLAG_VISIBLE 1
#define CURSOR_FLAG_SHAPE 2
#define CURSOR_HEADER 13
#define CURSOR_SHAPE_HEADER 8

static struct {
    int sent_any, last_visible, last_x, last_y;
    uint32_t last_shape;
    uint8_t *buf; /* 16 spare bytes, then the payload */
    size_t cap;
} g_cur;

static void put_be32(uint8_t *p, uint32_t v)
{
    p[0] = (uint8_t)(v >> 24);
    p[1] = (uint8_t)(v >> 16);
    p[2] = (uint8_t)(v >> 8);
    p[3] = (uint8_t)v;
}

static void put_be16(uint8_t *p, uint32_t v)
{
    p[0] = (uint8_t)(v >> 8);
    p[1] = (uint8_t)v;
}

/* Sends the cursor when it moved, changed shape or visibility. */
static void relay_cursor(const x11cap *cap)
{
    x11_cursor c;
    if (!x11_cursor_get(cap, &c))
        return;
    int w = c.w > CURSOR_MAX_SIDE ? CURSOR_MAX_SIDE : c.w;
    int h = c.h > CURSOR_MAX_SIDE ? CURSOR_MAX_SIDE : c.h;
    int visible = c.visible && w > 0 && h > 0;
    if (g_cur.sent_any && visible == g_cur.last_visible &&
        (!visible || (c.x == g_cur.last_x && c.y == g_cur.last_y && c.shape_id == g_cur.last_shape)))
        return;
    int have_shape = c.argb && w > 0 && h > 0 && (!g_cur.sent_any || c.shape_id != g_cur.last_shape);
    size_t len = CURSOR_HEADER + (have_shape ? CURSOR_SHAPE_HEADER + (size_t)w * h * 4 : 0);
    if (16 + len > g_cur.cap) {
        uint8_t *nb = realloc(g_cur.buf, 16 + len);
        if (!nb) {
            LOG("out of memory");
            exit(1);
        }
        g_cur.buf = nb;
        g_cur.cap = 16 + len;
    }
    uint8_t *p = g_cur.buf + 16;
    p[0] = (uint8_t)((visible ? CURSOR_FLAG_VISIBLE : 0) | (have_shape ? CURSOR_FLAG_SHAPE : 0));
    put_be32(p + 1, (uint32_t)c.x);
    put_be32(p + 5, (uint32_t)c.y);
    put_be32(p + 9, c.shape_id);
    if (have_shape) {
        uint8_t *q = p + CURSOR_HEADER;
        put_be16(q, (uint32_t)w);
        put_be16(q + 2, (uint32_t)h);
        put_be16(q + 4, c.hot_x < 0 ? 0 : (uint32_t)c.hot_x);
        put_be16(q + 6, c.hot_y < 0 ? 0 : (uint32_t)c.hot_y);
        uint8_t *d = q + CURSOR_SHAPE_HEADER;
        for (int y = 0; y < h; y++) {
            const uint32_t *src = c.argb + (size_t)y * c.w;
            for (int x = 0; x < w; x++, d += 4) {
                /* X gives premultiplied ARGB; the wire wants straight alpha. */
                uint32_t px = src[x], a = px >> 24;
                uint32_t b = px & 0xff, g = (px >> 8) & 0xff, r = (px >> 16) & 0xff;
                if (a != 0 && a != 255) {
                    b = b * 255 / a;
                    g = g * 255 / a;
                    r = r * 255 / a;
                    b = b > 255 ? 255 : b;
                    g = g > 255 ? 255 : g;
                    r = r > 255 ? 255 : r;
                }
                d[0] = (uint8_t)b;
                d[1] = (uint8_t)g;
                d[2] = (uint8_t)r;
                d[3] = (uint8_t)a;
            }
        }
        g_cur.last_shape = c.shape_id;
    }
    send_frame(p, len, KIND_CURSOR, 0, atomic_load(&g_cmd.kick_seq), now_us());
    g_cur.sent_any = 1;
    g_cur.last_visible = visible;
    g_cur.last_x = c.x;
    g_cur.last_y = c.y;
}

static void emit_video(session *s, int force_idr, uint32_t seq, uint64_t t0)
{
    uint8_t *au;
    size_t len;
    int idr;
    if (va_enc_encode(s->enc, force_idr, &au, &len, &idr) != 0) {
        LOG("encode failed");
        exit(1);
    }
    send_frame(au, len, KIND_AU, idr ? 1 : 0, seq, t0);
    g_win.send_us[g_win.sent % SEND_RING] = now_us();
    g_win.sent++;
}

static void enter_tile(session *s, uint32_t seq, uint64_t t0)
{
    s->mode = 1;
    s->low_run = s->high_run = s->any_changed = 0;
    send_mode(1);
    tile_dirty_all(s->tiles);
    emit_tile_packets(s, 1, seq, t0);
}

static void enter_video(session *s, uint32_t seq, uint64_t t0)
{
    s->mode = 0;
    s->low_run = s->high_run = s->any_changed = 0;
    send_mode(0);
    rect_t all = {0, 0, s->w, s->h};
    va_enc_convert(s->enc, x11_frame(s->cap), s->w * 4, all);
    emit_video(s, 1, seq, t0);
}

static void apply_mode_request(session *s)
{
    int req = atomic_exchange(&g_cmd.mode_req, 0);
    if (!req || s->o.tiles_policy == POLICY_VIDEO)
        return;
    s->policy = req - 1;
    uint64_t t0 = now_us();
    uint32_t seq = atomic_load(&g_cmd.kick_seq);
    if (s->policy == POLICY_TILE && s->mode == 0)
        enter_tile(s, seq, t0);
    else if (s->policy == POLICY_VIDEO && s->mode == 1)
        enter_video(s, seq, t0);
}

static struct {
    int on;
    uint64_t grab, diff, convert, encode, n, since;
} g_trace;

static void trace_flush(uint64_t now)
{
    if (!g_trace.on || now - g_trace.since < 2000000 || !g_trace.n)
        return;
    LOG("stages avg over %llu captures: grab %lluus tilediff %lluus convert %lluus encode+write %lluus",
        (unsigned long long)g_trace.n, (unsigned long long)(g_trace.grab / g_trace.n),
        (unsigned long long)(g_trace.diff / g_trace.n), (unsigned long long)(g_trace.convert / g_trace.n),
        (unsigned long long)(g_trace.encode / g_trace.n));
    memset(&g_trace.grab, 0, 5 * sizeof(uint64_t));
    g_trace.since = now;
}

static void handle_capture(session *s, uint32_t seq, uint64_t t0, int first)
{
    rect_t d;
    uint64_t ta = now_us();
    if (x11_capture(s->cap, &d) != 0)
        exit(1);
    uint64_t tb = now_us();
    const uint8_t *frame = x11_frame(s->cap);
    int dirty_tiles = 0;
    if (s->tiles) {
        rect_t all = {0, 0, s->w, s->h};
        dirty_tiles = tile_diff(s->tiles, frame, s->w * 4, first ? all : d);
    }
    uint64_t tc = now_us();
    g_trace.grab += tb - ta;
    g_trace.diff += tc - tb;
    g_trace.n++;
    double f = s->tiles ? (double)dirty_tiles / tile_count(s->tiles) : 0;

    if (s->mode == 0) {
        if (!rect_empty(&d))
            va_enc_convert(s->enc, frame, s->w * 4, d);
        uint64_t td = now_us();
        g_trace.convert += td - tc;
        if (!rect_empty(&d) || first)
            emit_video(s, first || atomic_exchange(&g_cmd.force_idr, 0), seq, t0);
        g_trace.encode += now_us() - td;
        trace_flush(now_us());
        if (s->tiles)
            tile_clear(s->tiles);
        if (s->policy == POLICY_AUTO && s->tiles) {
            if (f < 0.12) {
                s->low_run++;
                if (dirty_tiles > 0 && !first)
                    s->any_changed = 1;
            } else {
                s->low_run = 0;
                s->any_changed = 0;
            }
            if (s->low_run >= 4 && s->any_changed)
                enter_tile(s, seq, now_us());
        }
    } else {
        int leaving = 0;
        if (s->policy == POLICY_AUTO) {
            if (f > 0.25)
                s->high_run++;
            else
                s->high_run = 0;
            leaving = s->high_run >= 2;
        }
        if (first) {
            tile_dirty_all(s->tiles);
            emit_tile_packets(s, 1, seq, t0);
        } else if (leaving) {
            tile_clear(s->tiles);
            enter_video(s, seq, t0);
        } else if (dirty_tiles > 0) {
            emit_tile_packets(s, 0, seq, t0);
        }
    }
}

static int run_capture(const options *o)
{
    alarm(5);
    session s;
    memset(&s, 0, sizeof s);
    s.o = *o;
    g_trace.on = getenv("DRAWSPLAY_MEDIA_TRACE") != NULL;
    s.policy = o->tiles_policy;

    s.cap = x11_open(o->display, o->x, o->y, o->w, o->h, o->damage, o->cursor);
    if (!s.cap)
        return 1;
    x11_set_cursor_overlay(s.cap, o->cursor_overlay);
    s.w = x11_width(s.cap);
    s.h = x11_height(s.cap);
    s.enc = va_enc_open(o->render_node, s.w, s.h, o->fps, o->kbps, o->gop, NULL);
    if (!s.enc)
        return 1;
    if (o->tiles_policy != POLICY_VIDEO) {
        s.tiles = tile_open(s.w, s.h);
        if (!s.tiles) {
            LOG("cannot allocate tile state");
            return 1;
        }
    }

    g_cmd.wake_fd = eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC);
    if (g_cmd.wake_fd < 0) {
        LOG("eventfd failed");
        return 1;
    }
    pthread_t th;
    if (pthread_create(&th, NULL, stdin_thread, NULL) != 0) {
        LOG("cannot start stdin thread");
        return 1;
    }
    pthread_detach(th);

    const uint64_t interval = 1000000u / (uint64_t)o->fps;
    const uint64_t keepalive = (uint64_t)o->keepalive_ms * 1000u;
    uint64_t last_start = 0, last_send = 0;
    int first = 1;
    int have_frame = 0;

    if (s.policy == POLICY_TILE) {
        send_mode(1);
        s.mode = 1;
    }

    for (;;) {
        if (atomic_load(&g_cmd.quit))
            break;
        int kbps = atomic_exchange(&g_cmd.new_kbps, 0);
        if (kbps)
            va_enc_set_bitrate(s.enc, kbps);

        if (o->cursor)
            x11_poll_pointer(s.cap);
        x11_drain(s.cap);
        int pending = x11_pending(s.cap);
        apply_mode_request(&s);
        /* Only once the stream has opened with its first picture: the host
         * discards cursor frames ahead of it, and the first one sent must
         * carry the full cursor state. */
        if (o->cursor_overlay && have_frame)
            relay_cursor(s.cap);

        uint64_t now = now_us();
        if (have_frame && atomic_load(&g_cmd.force_idr)) {
            atomic_store(&g_cmd.force_idr, 0);
            uint32_t seq = atomic_load(&g_cmd.kick_seq);
            if (s.mode == 1) {
                tile_dirty_all(s.tiles);
                emit_tile_packets(&s, 1, seq, now);
            } else
                emit_video(&s, 1, seq, now);
            last_send = now_us();
            continue;
        }

        /* Video mode waits while the tablet has max_inflight frames in hand. */
        int gated = s.mode == 0 && !first && !window_open(o);

        int due_capture = first || (o->damage ? pending : 1);
        uint64_t next_ok = last_start + interval;
        if (due_capture && !first && now < next_ok)
            due_capture = 0;
        if (!o->damage && !first && now < next_ok)
            due_capture = 0;
        if (gated)
            due_capture = 0;

        if (due_capture) {
            uint64_t t0 = now;
            uint32_t seq = atomic_load(&g_cmd.kick_seq);
            last_start = t0;
            if (!o->damage)
                x11_mark_full(s.cap);
            handle_capture(&s, seq, t0, first);
            if (first) {
                alarm(0);
                first = 0;
                have_frame = 1;
            }
            last_send = now_us();
            continue;
        }

        if (keepalive && s.mode == 0 && have_frame && !gated && now - last_send >= keepalive) {
            uint64_t t0 = now;
            emit_video(&s, 0, atomic_load(&g_cmd.kick_seq), t0);
            last_send = now_us();
            continue;
        }

        int64_t wait = -1;
        if (o->damage && pending)
            wait = (int64_t)(next_ok - now);
        else if (!o->damage)
            wait = (int64_t)(next_ok - now);
        if (keepalive && s.mode == 0) {
            int64_t k = (int64_t)(last_send + keepalive - now);
            if (wait < 0 || k < wait)
                wait = k;
        }
        if (gated) {
            /* An ack wakes the loop through wake_fd; the timeout only
             * bounds the wait for the stall guard. */
            int64_t k = (int64_t)(g_win.full_since + ACK_STALL_US) - (int64_t)now;
            if (k < 1000)
                k = 1000;
            wait = k;
        }
        if (o->cursor && (wait < 0 || wait > POINTER_POLL_US))
            wait = POINTER_POLL_US;

        struct pollfd fds[2] = {{x11_fd(s.cap), POLLIN, 0}, {g_cmd.wake_fd, POLLIN, 0}};
        struct timespec ts, *tsp = NULL;
        if (wait >= 0) {
            ts.tv_sec = wait / 1000000;
            ts.tv_nsec = (wait % 1000000) * 1000;
            tsp = &ts;
        }
        int pr = ppoll(fds, 2, tsp, NULL);
        if (pr < 0 && errno != EINTR) {
            LOG("poll failed: %s", strerror(errno));
            return 1;
        }
        if (fds[1].revents & POLLIN) {
            uint64_t v;
            if (read(g_cmd.wake_fd, &v, sizeof v) < 0 && errno != EAGAIN) {
            }
        }
    }

    tile_close(s.tiles);
    va_enc_close(s.enc);
    x11_close(s.cap);
    return 0;
}

int main(int argc, char **argv)
{
    options o;
    signal(SIGPIPE, SIG_IGN);
    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_handler = on_alarm;
    sigaction(SIGALRM, &sa, NULL);

    if (parse_options(argc, argv, &o) != 0)
        return usage();
    if (o.probe)
        return run_probe(&o);
    if (o.selftest)
        return run_selftest(&o);
    return run_capture(&o);
}
