#include "va_encode.h"

#include <fcntl.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include <va/va.h>
#include <va/va_drm.h>
#include <va/va_enc_h264.h>

#define LOG2_MAX_FRAME_NUM 8
#define MAX_SURFACES 3
#define MAX_BUFFERS 24

typedef struct {
    uint8_t *buf;
    size_t cap;
    size_t pos;
} bitw;

struct va_enc {
    int fd;
    VADisplay dpy;
    VAConfigID cfg;
    VAContextID ctx;
    VASurfaceID surf[MAX_SURFACES];
    int have_surf, have_ctx, have_cfg;
    VABufferID coded;
    VAImage img;
    int have_img, have_coded;

    VAProfile profile;
    VAEntrypoint entrypoint;
    int high;
    int rc_mode;
    int pk_seq, pk_pic, pk_slice;

    int w, h, cw, ch, mbw, mbh, fps, kbps, gop;
    int rc_dirty;

    uint8_t *nv12;
    rect_t upload;
    int first_upload;

    int frame_num, since_idr, idr_id;
    int ref_valid, ref_frame_num, ref_poc, rec_idx;
    int ref_idx;

    uint8_t *out;
    size_t out_cap;
    uint8_t sps_pps[1024];
    size_t sps_pps_len;
    int have_sps_pps;
};

static void bw_init(bitw *b, uint8_t *buf, size_t cap)
{
    b->buf = buf;
    b->cap = cap;
    b->pos = 0;
    memset(buf, 0, cap);
}

static void put_bits(bitw *b, unsigned n, uint32_t v)
{
    for (unsigned i = n; i-- > 0;) {
        if (b->pos / 8 >= b->cap)
            return;
        if ((v >> i) & 1)
            b->buf[b->pos / 8] |= (uint8_t)(0x80 >> (b->pos % 8));
        b->pos++;
    }
}

static void put_ue(bitw *b, uint32_t v)
{
    uint64_t x = (uint64_t)v + 1;
    unsigned len = 0;
    while ((x >> len) > 1)
        len++;
    put_bits(b, len, 0);
    put_bits(b, len + 1, (uint32_t)x);
}

static void put_se(bitw *b, int32_t v)
{
    put_ue(b, v > 0 ? (uint32_t)(2 * v - 1) : (uint32_t)(-2 * v));
}

static void put_trailing(bitw *b)
{
    put_bits(b, 1, 1);
    while (b->pos % 8)
        put_bits(b, 1, 0);
}

/* Annex-B NAL with start code and emulation prevention. */
static size_t nal_wrap(uint8_t *out, size_t cap, int ref_idc, int type, const uint8_t *rbsp, size_t n)
{
    size_t o = 0;
    if (cap < n * 3 / 2 + 8)
        return 0;
    out[o++] = 0;
    out[o++] = 0;
    out[o++] = 0;
    out[o++] = 1;
    out[o++] = (uint8_t)(ref_idc << 5 | type);
    int zeros = 0;
    for (size_t i = 0; i < n; i++) {
        if (zeros >= 2 && rbsp[i] <= 3) {
            out[o++] = 3;
            zeros = 0;
        }
        out[o++] = rbsp[i];
        zeros = rbsp[i] == 0 ? zeros + 1 : 0;
    }
    return o;
}

uint32_t va_nal_mask(const uint8_t *buf, size_t len)
{
    uint32_t m = 0;
    for (size_t i = 0; i + 3 < len; i++) {
        if (buf[i] == 0 && buf[i + 1] == 0 && buf[i + 2] == 1) {
            m |= 1u << (buf[i + 3] & 31);
            i += 3;
        }
    }
    return m;
}

static size_t build_sps(const va_enc *e, uint8_t *out, size_t cap)
{
    uint8_t rbsp[256];
    bitw b;
    bw_init(&b, rbsp, sizeof rbsp);
    put_bits(&b, 8, e->high ? 100 : 77);
    put_bits(&b, 8, 0);
    put_bits(&b, 8, 52);
    put_ue(&b, 0);
    if (e->high) {
        put_ue(&b, 1);
        put_ue(&b, 0);
        put_ue(&b, 0);
        put_bits(&b, 1, 0);
        put_bits(&b, 1, 0);
    }
    put_ue(&b, LOG2_MAX_FRAME_NUM - 4);
    put_ue(&b, 2);
    put_ue(&b, 1);
    put_bits(&b, 1, 0);
    put_ue(&b, (uint32_t)e->mbw - 1);
    put_ue(&b, (uint32_t)e->mbh - 1);
    put_bits(&b, 1, 1);
    put_bits(&b, 1, 1);
    int crop_r = (e->cw - e->w) / 2, crop_b = (e->ch - e->h) / 2;
    if (crop_r || crop_b) {
        put_bits(&b, 1, 1);
        put_ue(&b, 0);
        put_ue(&b, (uint32_t)crop_r);
        put_ue(&b, 0);
        put_ue(&b, (uint32_t)crop_b);
    } else {
        put_bits(&b, 1, 0);
    }
    put_bits(&b, 1, 1);
    put_bits(&b, 1, 0);
    put_bits(&b, 1, 0);
    put_bits(&b, 1, 1);
    put_bits(&b, 3, 5);
    put_bits(&b, 1, 0);
    put_bits(&b, 1, 1);
    put_bits(&b, 8, 1);
    put_bits(&b, 8, 1);
    put_bits(&b, 8, 1);
    put_bits(&b, 1, 0);
    put_bits(&b, 1, 0);
    put_bits(&b, 1, 0);
    put_bits(&b, 1, 0);
    put_bits(&b, 1, 0);
    put_bits(&b, 1, 1);
    put_bits(&b, 1, 1);
    put_ue(&b, 0);
    put_ue(&b, 0);
    put_ue(&b, 15);
    put_ue(&b, 15);
    put_ue(&b, 0);
    put_ue(&b, 1);
    put_trailing(&b);
    return nal_wrap(out, cap, 3, 7, rbsp, b.pos / 8);
}

static size_t build_pps(const va_enc *e, uint8_t *out, size_t cap)
{
    uint8_t rbsp[64];
    bitw b;
    bw_init(&b, rbsp, sizeof rbsp);
    put_ue(&b, 0);
    put_ue(&b, 0);
    put_bits(&b, 1, 1);
    put_bits(&b, 1, 0);
    put_ue(&b, 0);
    put_ue(&b, 0);
    put_ue(&b, 0);
    put_bits(&b, 1, 0);
    put_bits(&b, 2, 0);
    put_se(&b, 0);
    put_se(&b, 0);
    put_se(&b, 0);
    put_bits(&b, 1, 1);
    put_bits(&b, 1, 0);
    put_bits(&b, 1, 0);
    if (e->high) {
        put_bits(&b, 1, 1);
        put_bits(&b, 1, 0);
        put_se(&b, 0);
    }
    put_trailing(&b);
    return nal_wrap(out, cap, 3, 8, rbsp, b.pos / 8);
}

static size_t build_slice_header(const va_enc *e, int idr, uint8_t *out, size_t cap)
{
    uint8_t rbsp[64];
    bitw b;
    bw_init(&b, rbsp, sizeof rbsp);
    put_ue(&b, 0);
    put_ue(&b, idr ? 7 : 5);
    put_ue(&b, 0);
    put_bits(&b, LOG2_MAX_FRAME_NUM, (uint32_t)e->frame_num);
    if (idr)
        put_ue(&b, (uint32_t)e->idr_id);
    if (!idr) {
        put_bits(&b, 1, 0);
        put_bits(&b, 1, 0);
        put_bits(&b, 1, 0);
        put_ue(&b, 0);
    } else {
        put_bits(&b, 1, 0);
        put_bits(&b, 1, 0);
    }
    put_se(&b, 0);
    put_ue(&b, 0);
    put_se(&b, 0);
    put_se(&b, 0);
    while (b.pos % 8)
        put_bits(&b, 1, 1);
    return nal_wrap(out, cap, 3, idr ? 5 : 1, rbsp, b.pos / 8);
}

static const char *va_err(VAStatus s)
{
    return vaErrorStr(s);
}

#define VA_CHECK(call, what)                                                                                          \
    do {                                                                                                              \
        VAStatus s_ = (call);                                                                                         \
        if (s_ != VA_STATUS_SUCCESS) {                                                                                \
            LOG("%s failed: %s", what, va_err(s_));                                                                   \
            goto fail;                                                                                                \
        }                                                                                                             \
    } while (0)

static void destroy(va_enc *e)
{
    if (!e)
        return;
    if (e->dpy) {
        if (e->have_img)
            vaDestroyImage(e->dpy, e->img.image_id);
        if (e->have_coded)
            vaDestroyBuffer(e->dpy, e->coded);
        if (e->have_ctx)
            vaDestroyContext(e->dpy, e->ctx);
        if (e->have_surf)
            vaDestroySurfaces(e->dpy, e->surf, MAX_SURFACES);
        if (e->have_cfg)
            vaDestroyConfig(e->dpy, e->cfg);
        vaTerminate(e->dpy);
    }
    if (e->fd >= 0)
        close(e->fd);
    free(e->nv12);
    free(e->out);
    free(e);
}

void va_enc_close(va_enc *e)
{
    destroy(e);
}

static int try_config(va_enc *e, VAProfile profile, VAEntrypoint ep)
{
    VAEntrypoint eps[16];
    int n = 0;
    if (vaQueryConfigEntrypoints(e->dpy, profile, eps, &n) != VA_STATUS_SUCCESS)
        return -1;
    int found = 0;
    for (int i = 0; i < n; i++)
        found |= eps[i] == ep;
    if (!found)
        return -1;

    VAConfigAttrib at[3] = {{VAConfigAttribRTFormat, 0}, {VAConfigAttribRateControl, 0},
                            {VAConfigAttribEncPackedHeaders, 0}};
    if (vaGetConfigAttributes(e->dpy, profile, ep, at, 3) != VA_STATUS_SUCCESS)
        return -1;
    if (at[0].value == VA_ATTRIB_NOT_SUPPORTED || !(at[0].value & VA_RT_FORMAT_YUV420)) {
        LOG("profile %d entrypoint %d: no YUV420 render target", (int)profile, (int)ep);
        return -1;
    }
    int rc = 0;
    if (at[1].value != VA_ATTRIB_NOT_SUPPORTED) {
        if (at[1].value & VA_RC_VBR)
            rc = VA_RC_VBR;
        else if (at[1].value & VA_RC_CBR)
            rc = VA_RC_CBR;
    }
    if (!rc) {
        LOG("profile %d entrypoint %d: no VBR/CBR rate control (mask 0x%x)", (int)profile, (int)ep, at[1].value);
        return -1;
    }
    unsigned pk = 0;
    if (at[2].value != VA_ATTRIB_NOT_SUPPORTED)
        pk = at[2].value & (VA_ENC_PACKED_HEADER_SEQUENCE | VA_ENC_PACKED_HEADER_PICTURE | VA_ENC_PACKED_HEADER_SLICE);

    VAConfigAttrib set[3] = {{VAConfigAttribRTFormat, VA_RT_FORMAT_YUV420}, {VAConfigAttribRateControl, (unsigned)rc},
                             {VAConfigAttribEncPackedHeaders, pk}};
    if (vaCreateConfig(e->dpy, profile, ep, set, 3, &e->cfg) != VA_STATUS_SUCCESS) {
        LOG("profile %d entrypoint %d: vaCreateConfig rejected rc 0x%x packed 0x%x", (int)profile, (int)ep, rc, pk);
        return -1;
    }
    e->have_cfg = 1;
    e->profile = profile;
    e->entrypoint = ep;
    e->high = profile == VAProfileH264High;
    e->rc_mode = rc;
    e->pk_seq = !!(pk & VA_ENC_PACKED_HEADER_SEQUENCE);
    e->pk_pic = !!(pk & VA_ENC_PACKED_HEADER_PICTURE);
    e->pk_slice = !!(pk & VA_ENC_PACKED_HEADER_SLICE);
    return 0;
}

static va_enc *open_node(const char *node, int w, int h, int fps, int kbps, int gop, va_info *info)
{
    static const struct {
        VAEntrypoint ep;
        const char *name;
    } eps[] = {{VAEntrypointEncSliceLP, "EncSliceLP"}, {VAEntrypointEncSlice, "EncSlice"}};
    static const struct {
        VAProfile p;
        const char *name;
    } profs[] = {{VAProfileH264High, "High"}, {VAProfileH264Main, "Main"}};

    va_enc *e = calloc(1, sizeof *e);
    if (!e)
        return NULL;
    e->fd = -1;
    e->fd = open(node, O_RDWR | O_CLOEXEC);
    if (e->fd < 0) {
        LOG("cannot open %s", node);
        goto fail;
    }
    e->dpy = vaGetDisplayDRM(e->fd);
    if (!e->dpy) {
        LOG("vaGetDisplayDRM failed on %s", node);
        goto fail;
    }
    int major, minor;
    VA_CHECK(vaInitialize(e->dpy, &major, &minor), "vaInitialize");
    const char *vendor = vaQueryVendorString(e->dpy);
    if (info) {
        snprintf(info->vendor, sizeof info->vendor, "%s", vendor ? vendor : "unknown");
        for (char *p = info->vendor; *p; p++) {
            if (*p == '"' || *p == '\\' || (unsigned char)*p < 0x20)
                *p = ' ';
        }
    }

    int got = 0;
    for (size_t i = 0; i < 2 && !got; i++) {
        for (size_t j = 0; j < 2 && !got; j++) {
            if (try_config(e, profs[j].p, eps[i].ep) == 0) {
                got = 1;
                if (info) {
                    info->entrypoint = eps[i].name;
                    info->profile = profs[j].name;
                }
            }
        }
    }
    if (!got) {
        LOG("%s has no usable H.264 encode entrypoint (vendor: %s)", node, vendor ? vendor : "unknown");
        goto fail;
    }

    e->w = w;
    e->h = h;
    e->cw = (w + 15) & ~15;
    e->ch = (h + 15) & ~15;
    e->mbw = e->cw / 16;
    e->mbh = e->ch / 16;
    e->fps = fps;
    e->kbps = kbps;
    e->gop = gop > 0 ? gop : 1;
    e->rc_dirty = 1;

    VA_CHECK(vaCreateSurfaces(e->dpy, VA_RT_FORMAT_YUV420, (unsigned)e->cw, (unsigned)e->ch, e->surf, MAX_SURFACES,
                              NULL, 0),
             "vaCreateSurfaces");
    e->have_surf = 1;
    VA_CHECK(vaCreateContext(e->dpy, e->cfg, e->cw, e->ch, VA_PROGRESSIVE, e->surf, MAX_SURFACES, &e->ctx),
             "vaCreateContext");
    e->have_ctx = 1;

    size_t coded_size = (size_t)e->cw * e->ch * 3 / 2 + 4096;
    VA_CHECK(vaCreateBuffer(e->dpy, e->ctx, VAEncCodedBufferType, (unsigned)coded_size, 1, NULL, &e->coded),
             "vaCreateBuffer(coded)");
    e->have_coded = 1;

    int nfmt = vaMaxNumImageFormats(e->dpy);
    VAImageFormat *fmts = calloc((size_t)nfmt, sizeof *fmts);
    int cnt = 0, ok = 0;
    VAImageFormat nv12 = {0};
    if (fmts && vaQueryImageFormats(e->dpy, fmts, &cnt) == VA_STATUS_SUCCESS) {
        for (int i = 0; i < cnt; i++) {
            if (fmts[i].fourcc == VA_FOURCC_NV12) {
                nv12 = fmts[i];
                ok = 1;
                break;
            }
        }
    }
    free(fmts);
    if (!ok) {
        LOG("driver exposes no NV12 VAImage format");
        goto fail;
    }
    VA_CHECK(vaCreateImage(e->dpy, &nv12, e->cw, e->ch, &e->img), "vaCreateImage");
    e->have_img = 1;

    size_t nv_size = (size_t)e->cw * e->ch * 3 / 2;
    e->nv12 = malloc(nv_size);
    e->out_cap = VA_FRAME_HEADROOM + coded_size + sizeof e->sps_pps;
    e->out = malloc(e->out_cap);
    if (!e->nv12 || !e->out) {
        LOG("out of memory");
        goto fail;
    }
    memset(e->nv12, 16, (size_t)e->cw * e->ch);
    memset(e->nv12 + (size_t)e->cw * e->ch, 128, (size_t)e->cw * e->ch / 2);
    e->first_upload = 1;
    e->upload.w = e->upload.h = 0;
    e->ref_idx = -1;
    e->rec_idx = 1;

    size_t sl = build_sps(e, e->sps_pps, sizeof e->sps_pps);
    size_t pl = build_pps(e, e->sps_pps + sl, sizeof e->sps_pps - sl);
    e->sps_pps_len = sl + pl;
    return e;

fail:
    destroy(e);
    return NULL;
}

va_enc *va_enc_open(const char *node, int w, int h, int fps, int kbps, int gop, va_info *info)
{
    if (node)
        return open_node(node, w, h, fps, kbps, gop, info);
    char path[64];
    for (int i = 128; i < 144; i++) {
        snprintf(path, sizeof path, "/dev/dri/renderD%d", i);
        if (access(path, R_OK | W_OK) != 0)
            continue;
        va_enc *e = open_node(path, w, h, fps, kbps, gop, info);
        if (e)
            return e;
    }
    LOG("no render node with a usable VA-API H.264 encoder found");
    return NULL;
}

void va_enc_set_bitrate(va_enc *e, int kbps)
{
    if (kbps < 100)
        kbps = 100;
    if (kbps != e->kbps) {
        e->kbps = kbps;
        e->rc_dirty = 1;
    }
}

#if defined(__x86_64__) && defined(__GNUC__) && !defined(__clang__)
#define MULTIVERSION __attribute__((target_clones("avx2", "default")))
#else
#define MULTIVERSION
#endif

/* BT.709 limited range. 16-bit lane math so the loops vectorize with plain SSE2. */
MULTIVERSION static void convert_y_row(const uint8_t *restrict s, uint8_t *restrict y, int n)
{
    for (int i = 0; i < n; i++) {
        uint16_t b = s[4 * i], g = s[4 * i + 1], r = s[4 * i + 2];
        y[i] = (uint8_t)((uint16_t)(47 * r + 157 * g + 16 * b + 4224) >> 8);
    }
}

MULTIVERSION static void convert_c_row(const uint8_t *restrict a, const uint8_t *restrict b, uint8_t *restrict c, int n)
{
    for (int i = 0; i < n; i++) {
        const uint8_t *p = a + 8 * i, *q = b + 8 * i;
        int16_t bb = (int16_t)((p[0] + p[4] + q[0] + q[4] + 2) >> 2);
        int16_t gg = (int16_t)((p[1] + p[5] + q[1] + q[5] + 2) >> 2);
        int16_t rr = (int16_t)((p[2] + p[6] + q[2] + q[6] + 2) >> 2);
        int16_t u = (int16_t)((int16_t)(-26 * rr - 87 * gg + 113 * bb + 128) >> 8);
        int16_t v = (int16_t)((int16_t)(112 * rr - 102 * gg - 10 * bb + 128) >> 8);
        c[2 * i] = (uint8_t)(u + 128);
        c[2 * i + 1] = (uint8_t)(v + 128);
    }
}

void va_enc_convert(va_enc *e, const uint8_t *bgra, int stride, rect_t r)
{
    r = rect_clip(r, e->w, e->h);
    if (rect_empty(&r))
        return;
    r.x &= ~1;
    r.y &= ~1;
    r.w = (r.w + 1) & ~1;
    r.h = (r.h + 1) & ~1;
    uint8_t *yp = e->nv12;
    uint8_t *uvp = e->nv12 + (size_t)e->cw * e->ch;
    for (int y = r.y; y < r.y + r.h; y += 2) {
        const uint8_t *s0 = bgra + (size_t)y * stride + (size_t)r.x * 4;
        const uint8_t *s1 = s0 + stride;
        uint8_t *y0 = yp + (size_t)y * e->cw + r.x;
        convert_y_row(s0, y0, r.w);
        convert_y_row(s1, y0 + e->cw, r.w);
        convert_c_row(s0, s1, uvp + (size_t)(y / 2) * e->cw + r.x, r.w / 2);
    }
    e->upload = rect_union(e->upload, r);
}

static int upload(va_enc *e)
{
    rect_t r = e->upload;
    if (e->first_upload) {
        r.x = r.y = 0;
        r.w = e->cw;
        r.h = e->ch;
    }
    if (rect_empty(&r))
        return 0;
    void *p;
    VAStatus s = vaMapBuffer(e->dpy, e->img.buf, &p);
    if (s != VA_STATUS_SUCCESS) {
        LOG("vaMapBuffer(image) failed: %s", va_err(s));
        return -1;
    }
    uint8_t *dst = p;
    const uint8_t *yp = e->nv12, *uvp = e->nv12 + (size_t)e->cw * e->ch;
    for (int y = r.y; y < r.y + r.h; y++)
        memcpy(dst + e->img.offsets[0] + (size_t)y * e->img.pitches[0] + r.x, yp + (size_t)y * e->cw + r.x,
               (size_t)r.w);
    for (int y = r.y / 2; y < (r.y + r.h + 1) / 2; y++)
        memcpy(dst + e->img.offsets[1] + (size_t)y * e->img.pitches[1] + r.x, uvp + (size_t)y * e->cw + r.x,
               (size_t)r.w);
    vaUnmapBuffer(e->dpy, e->img.buf);
    s = vaPutImage(e->dpy, e->surf[0], e->img.image_id, r.x, r.y, (unsigned)r.w, (unsigned)r.h, r.x, r.y,
                   (unsigned)r.w, (unsigned)r.h);
    if (s != VA_STATUS_SUCCESS) {
        LOG("vaPutImage failed: %s", va_err(s));
        return -1;
    }
    e->upload.x = e->upload.y = e->upload.w = e->upload.h = 0;
    e->first_upload = 0;
    return 0;
}

typedef struct {
    VABufferID ids[MAX_BUFFERS];
    int n;
} bufset;

static int add_buf(va_enc *e, bufset *bs, VABufferType type, size_t size, const void *data)
{
    if (bs->n >= MAX_BUFFERS)
        return -1;
    VAStatus s = vaCreateBuffer(e->dpy, e->ctx, type, (unsigned)size, 1, (void *)data, &bs->ids[bs->n]);
    if (s != VA_STATUS_SUCCESS) {
        LOG("vaCreateBuffer(type %d) failed: %s", (int)type, va_err(s));
        return -1;
    }
    bs->n++;
    return 0;
}

static int add_misc(va_enc *e, bufset *bs, VAEncMiscParameterType type, const void *payload, size_t size)
{
    uint8_t tmp[sizeof(VAEncMiscParameterBuffer) + 64];
    if (size > 64)
        return -1;
    VAEncMiscParameterBuffer *m = (VAEncMiscParameterBuffer *)tmp;
    m->type = type;
    memcpy(m->data, payload, size);
    return add_buf(e, bs, VAEncMiscParameterBufferType, sizeof(VAEncMiscParameterBuffer) + size, tmp);
}

static int add_packed(va_enc *e, bufset *bs, VAEncPackedHeaderType type, const uint8_t *data, size_t bytes)
{
    VAEncPackedHeaderParameterBuffer pp = {0};
    pp.type = type;
    pp.bit_length = (uint32_t)(bytes * 8);
    pp.has_emulation_bytes = 1;
    if (add_buf(e, bs, VAEncPackedHeaderParameterBufferType, sizeof pp, &pp) != 0)
        return -1;
    return add_buf(e, bs, VAEncPackedHeaderDataBufferType, bytes, data);
}

static void invalid_pic(VAPictureH264 *p)
{
    p->picture_id = VA_INVALID_SURFACE;
    p->frame_idx = 0;
    p->flags = VA_PICTURE_H264_INVALID;
    p->TopFieldOrderCnt = 0;
    p->BottomFieldOrderCnt = 0;
}

static int add_rate_buffers(va_enc *e, bufset *bs)
{
    VAEncMiscParameterRateControl rc = {0};
    rc.bits_per_second = (uint32_t)e->kbps * 1000u;
    rc.target_percentage = e->rc_mode == VA_RC_VBR ? 75 : 100;
    rc.window_size = 125;
    rc.initial_qp = 0;
    rc.min_qp = 0;
    VAEncMiscParameterHRD hrd = {0};
    hrd.buffer_size = (uint32_t)e->kbps * 125u;
    hrd.initial_buffer_fullness = hrd.buffer_size * 3 / 4;
    VAEncMiscParameterFrameRate fr = {0};
    fr.framerate = (uint32_t)e->fps | (1u << 16);
    if (add_misc(e, bs, VAEncMiscParameterTypeRateControl, &rc, sizeof rc) != 0 ||
        add_misc(e, bs, VAEncMiscParameterTypeHRD, &hrd, sizeof hrd) != 0 ||
        add_misc(e, bs, VAEncMiscParameterTypeFrameRate, &fr, sizeof fr) != 0)
        return -1;
    return 0;
}

int va_enc_encode(va_enc *e, int force_idr, uint8_t **au, size_t *len, int *is_idr)
{
    int idr = force_idr || !e->ref_valid || e->since_idr >= e->gop;
    bufset bs = {{0}, 0};
    int begun = 0;
    int rc = -1;

    if (upload(e) != 0)
        return -1;

    if (idr) {
        e->frame_num = 0;
        e->since_idr = 0;
        e->idr_id = (e->idr_id + 1) & 0xffff;
        e->ref_valid = 0;
    }
    int poc = 2 * e->since_idr;
    VASurfaceID rec = e->surf[e->rec_idx];

    VA_CHECK(vaBeginPicture(e->dpy, e->ctx, e->surf[0]), "vaBeginPicture");
    begun = 1;

    if (idr) {
        VAEncSequenceParameterBufferH264 sp;
        memset(&sp, 0, sizeof sp);
        sp.seq_parameter_set_id = 0;
        sp.level_idc = 52;
        sp.intra_period = (uint32_t)e->gop;
        sp.intra_idr_period = (uint32_t)e->gop;
        sp.ip_period = 1;
        sp.bits_per_second = (uint32_t)e->kbps * 1000u;
        sp.max_num_ref_frames = 1;
        sp.picture_width_in_mbs = (uint16_t)e->mbw;
        sp.picture_height_in_mbs = (uint16_t)e->mbh;
        sp.seq_fields.bits.chroma_format_idc = 1;
        sp.seq_fields.bits.frame_mbs_only_flag = 1;
        sp.seq_fields.bits.direct_8x8_inference_flag = 1;
        sp.seq_fields.bits.log2_max_frame_num_minus4 = LOG2_MAX_FRAME_NUM - 4;
        sp.seq_fields.bits.pic_order_cnt_type = 2;
        if (e->cw != e->w || e->ch != e->h) {
            sp.frame_cropping_flag = 1;
            sp.frame_crop_right_offset = (uint32_t)(e->cw - e->w) / 2;
            sp.frame_crop_bottom_offset = (uint32_t)(e->ch - e->h) / 2;
        }
        if (!e->pk_seq) {
            sp.vui_parameters_present_flag = 1;
            sp.vui_fields.bits.bitstream_restriction_flag = 1;
            sp.vui_fields.bits.log2_max_mv_length_horizontal = 15;
            sp.vui_fields.bits.log2_max_mv_length_vertical = 15;
        }
        if (add_buf(e, &bs, VAEncSequenceParameterBufferType, sizeof sp, &sp) != 0)
            goto fail;
    }
    if (idr || e->rc_dirty) {
        if (add_rate_buffers(e, &bs) != 0)
            goto fail;
        e->rc_dirty = 0;
    }
    if (idr && e->pk_seq) {
        uint8_t hdr[256];
        size_t n = build_sps(e, hdr, sizeof hdr);
        if (!n || add_packed(e, &bs, VAEncPackedHeaderSequence, hdr, n) != 0)
            goto fail;
    }

    VAEncPictureParameterBufferH264 pp;
    memset(&pp, 0, sizeof pp);
    pp.CurrPic.picture_id = rec;
    pp.CurrPic.frame_idx = (uint32_t)e->frame_num;
    pp.CurrPic.flags = 0;
    pp.CurrPic.TopFieldOrderCnt = poc;
    pp.CurrPic.BottomFieldOrderCnt = poc;
    for (int i = 0; i < 16; i++)
        invalid_pic(&pp.ReferenceFrames[i]);
    if (!idr) {
        VAPictureH264 *r = &pp.ReferenceFrames[0];
        r->picture_id = e->surf[e->ref_idx];
        r->frame_idx = (uint32_t)e->ref_frame_num;
        r->flags = VA_PICTURE_H264_SHORT_TERM_REFERENCE;
        r->TopFieldOrderCnt = e->ref_poc;
        r->BottomFieldOrderCnt = e->ref_poc;
    }
    pp.coded_buf = e->coded;
    pp.pic_parameter_set_id = 0;
    pp.seq_parameter_set_id = 0;
    pp.frame_num = (uint16_t)e->frame_num;
    pp.pic_init_qp = 26;
    pp.pic_fields.bits.idr_pic_flag = idr;
    pp.pic_fields.bits.reference_pic_flag = 1;
    pp.pic_fields.bits.entropy_coding_mode_flag = 1;
    pp.pic_fields.bits.transform_8x8_mode_flag = e->high;
    pp.pic_fields.bits.deblocking_filter_control_present_flag = 1;

    if (idr && e->pk_pic) {
        uint8_t hdr[64];
        size_t n = build_pps(e, hdr, sizeof hdr);
        if (!n || add_packed(e, &bs, VAEncPackedHeaderPicture, hdr, n) != 0)
            goto fail;
    }
    if (add_buf(e, &bs, VAEncPictureParameterBufferType, sizeof pp, &pp) != 0)
        goto fail;

    if (e->pk_slice) {
        uint8_t hdr[96];
        size_t n = build_slice_header(e, idr, hdr, sizeof hdr);
        if (!n || add_packed(e, &bs, VAEncPackedHeaderSlice, hdr, n) != 0)
            goto fail;
    }
    VAEncSliceParameterBufferH264 sl;
    memset(&sl, 0, sizeof sl);
    sl.macroblock_address = 0;
    sl.num_macroblocks = (uint32_t)(e->mbw * e->mbh);
    sl.macroblock_info = VA_INVALID_ID;
    sl.slice_type = idr ? 2 : 0;
    sl.idr_pic_id = (uint16_t)e->idr_id;
    sl.pic_order_cnt_lsb = (uint16_t)poc;
    for (int i = 0; i < 32; i++) {
        invalid_pic(&sl.RefPicList0[i]);
        invalid_pic(&sl.RefPicList1[i]);
    }
    if (!idr)
        sl.RefPicList0[0] = pp.ReferenceFrames[0];
    sl.disable_deblocking_filter_idc = 0;
    if (add_buf(e, &bs, VAEncSliceParameterBufferType, sizeof sl, &sl) != 0)
        goto fail;

    VA_CHECK(vaRenderPicture(e->dpy, e->ctx, bs.ids, bs.n), "vaRenderPicture");
    VA_CHECK(vaEndPicture(e->dpy, e->ctx), "vaEndPicture");
    begun = 0;
    VA_CHECK(vaSyncSurface(e->dpy, e->surf[0]), "vaSyncSurface");

    VACodedBufferSegment *seg = NULL;
    VA_CHECK(vaMapBuffer(e->dpy, e->coded, (void **)&seg), "vaMapBuffer(coded)");
    uint8_t *o = e->out + VA_FRAME_HEADROOM;
    size_t n = 0, room = e->out_cap - VA_FRAME_HEADROOM - e->sps_pps_len;
    int overflow = 0;
    for (; seg; seg = seg->next) {
        if (seg->status & VA_CODED_BUF_STATUS_SLICE_OVERFLOW_MASK)
            overflow = 1;
        if (n + seg->size > room) {
            overflow = 1;
            break;
        }
        memcpy(o + n, seg->buf, seg->size);
        n += seg->size;
    }
    vaUnmapBuffer(e->dpy, e->coded);
    if (overflow || n == 0) {
        LOG("encoder returned an unusable coded buffer (%zu bytes%s)", n, overflow ? ", overflow" : "");
        goto fail;
    }
    if (idr) {
        uint32_t m = va_nal_mask(o, n);
        if (!(m & (1u << 7)) || !(m & (1u << 8))) {
            memmove(o + e->sps_pps_len, o, n);
            memcpy(o, e->sps_pps, e->sps_pps_len);
            n += e->sps_pps_len;
        }
    }

    e->ref_idx = e->rec_idx;
    e->rec_idx = e->rec_idx == 1 ? 2 : 1;
    e->ref_frame_num = e->frame_num;
    e->ref_poc = poc;
    e->ref_valid = 1;
    e->frame_num = (e->frame_num + 1) & ((1 << LOG2_MAX_FRAME_NUM) - 1);
    e->since_idr++;
    *au = o;
    *len = n;
    *is_idr = idr;
    rc = 0;

fail:
    if (begun)
        vaEndPicture(e->dpy, e->ctx);
    for (int i = 0; i < bs.n; i++)
        vaDestroyBuffer(e->dpy, bs.ids[i]);
    if (rc != 0 && idr)
        e->ref_valid = 0;
    return rc;
}
