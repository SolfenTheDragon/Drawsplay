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

#define HELPER_VERSION 1
#define KIND_AU 1
#define KIND_TILE 2
#define KIND_MODE 3

#define POINTER_POLL_US 8000

enum { POLICY_AUTO, POLICY_VIDEO, POLICY_TILE };

typedef struct {
    int probe, selftest;
    const char *display, *render_node;
    int x, y, w, h, fps, kbps, gop;
    int tiles_policy;
    int damage, cursor;
    int keepalive_ms;
} options;

static struct {
    atomic_uint kick_seq;
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
        } else if (!strcmp(a, "--keepalive-ms")) {
            o->keepalive_ms = atoi(v);
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
    if (o->fps < 1 || o->fps > 240 || o->kbps < 100 || o->keepalive_ms < 0) {
        LOG("invalid --fps/--bitrate-kbps/--keepalive-ms");
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

        int due_capture = first || (o->damage ? pending : 1);
        uint64_t next_ok = last_start + interval;
        if (due_capture && !first && now < next_ok)
            due_capture = 0;
        if (!o->damage && !first && now < next_ok)
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

        if (keepalive && s.mode == 0 && have_frame && now - last_send >= keepalive) {
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
