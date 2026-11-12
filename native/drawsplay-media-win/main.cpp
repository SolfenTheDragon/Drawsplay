// drawsplay-media-win - Windows native capture/encode helper.
//
// Reads frames straight from the DrawSplay IddCx driver's shared swap-chain
// texture (drivers/windows/idd/Public.h, IOCTL_DRAWSPLAY_GET_FRAME_SURFACE),
// converts BGRA -> NV12 on the GPU with the D3D11 video processor and encodes
// with a hardware Media Foundation H.264 encoder on the same adapter. Nothing
// leaves the GPU until the encoder emits the bitstream.
//
// Latency/throughput control (the design SuperDisplay uses; see
// wiki/topics/superdisplay-windows-pipeline.md):
//   * The driver never waits for this process: it skips a frame when the
//     shared texture is still held here.
//   * Frames in flight are bounded. The host forwards the tablet's
//     cumulative frame acknowledgements ("A <n>" on stdin). A new frame is
//     encoded only while fewer than `window` encoded frames are
//     unacknowledged, where `window` covers one measured submit->ack round
//     trip (2 frames minimum, --max-inflight N maximum). Otherwise the newest
//     captured frame waits (older ones are overwritten) and is encoded as
//     soon as an ack opens the window, so a stall can queue at most about one
//     round trip of latency and the tablet always catches up to the newest
//     picture. Until the first ack arrives the window is off, so clients
//     without acks behave as before.
//   * Keyframes: gradual intra refresh where the encoder supports it; with
//     --keyframes on-demand (tablets that ask for keyframes when they drop
//     pictures) no periodic keyframes at all, so there are no periodic
//     bursts several frames in size on the link.
//   * Cursor: with --cursor overlay the driver's hardware cursor is
//     forwarded as small cursor frames (kind 4) for the tablet to draw, so
//     cursor motion costs no encode and never waits behind a video frame.
//
// Output and control protocol are those of native/drawsplay-media (Linux),
// specified in FUNCTIONALITY.md section 3.5:
// stdout carries frames with a 16-byte little-endian header
//   u32 payload length, u8 kind (1 = H.264 access unit), u8 flags
//   (1 = keyframe), u16 reserved, u32 input sequence, u32 pipeline us
// followed by the Annex-B access unit. stdin takes newline-terminated
// commands: "K <seq>" input kick, "A <n>" frames acknowledged by the tablet
// since this helper started, "I" force a keyframe, "B <kbps>" retune the
// bitrate, "Q" quit. stderr is a free-form log.
//
// Exit codes: 0 quit/stdin closed, 1 error, 3 display mode changed (the host
// restarts the pipeline, which reopens at the new size).

#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <winioctl.h>
#include <initguid.h>
#include <cfgmgr32.h>
#include <d3d11_4.h>
#include <dxgi1_6.h>
#include <mfapi.h>
#include <mfidl.h>
#include <mferror.h>
#include <mftransform.h>
#include <codecapi.h>
#include <strmif.h>
#include <wrl/client.h>

#include <share.h>
#include <timeapi.h>

#include <algorithm>
#include <atomic>
#include <cstdarg>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <deque>
#include <map>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

#include "../../drivers/windows/idd/Public.h"

using Microsoft::WRL::ComPtr;

namespace {

constexpr int kHelperVersion = 1;
constexpr uint8_t kKindAU = 1;
// Cursor update for the tablet to draw itself (--cursor overlay). Payload,
// big-endian: u8 flags (1 = visible, 2 = shape follows), i32 x, i32 y (top-
// left of the image, frame pixels), u32 shape id; then if a shape follows:
// u16 width, u16 height, u16 hot x, u16 hot y, width*height*4 BGRA bytes
// (straight alpha, rows packed).
constexpr uint8_t kKindCursor = 4;
constexpr uint8_t kFlagKeyframe = 1;
// Acks that stop arriving while the window is full would freeze the stream;
// after this long without progress the in-flight frames are written off.
// Short: a full window with no ack progress this long is a lost ack, and the
// guard's wait is a visible freeze (normal round trips are a few ms on USB
// and tens of ms on Wi-Fi).
constexpr int64_t kAckStallUs = 300000;
// A static screen is re-encoded a few times after it settles so the
// encoder can refine the last picture (SuperDisplay does the same).
constexpr int64_t kIdleRefreshAfterUs = 100000;
constexpr DWORD kFramePollMs = 1;
constexpr int64_t kSurfaceRecheckUs = 250000;
// The frame window is sized from the measured submit->ack round trip: just
// enough frames to keep the link busy, so a stall can queue at most about
// one round trip of latency. The minimum over a sliding 2-4 s span tracks
// the path's base delay while ignoring jitter spikes.
constexpr int kMinWindow = 2;
constexpr int64_t kRttSpanUs = 2000000;
constexpr size_t kSendRing = 512;

// Minimum of a signal over the last one to two spans.
struct WindowedMin {
    int64_t best = INT64_MAX, next = INT64_MAX, rollUs = 0;
    void Add(int64_t nowUs, int64_t v)
    {
        if (nowUs >= rollUs) {
            best = next;
            next = INT64_MAX;
            rollUs = nowUs + kRttSpanUs;
        }
        next = std::min(next, v);
        best = std::min(best, v);
    }
    int64_t Get() const { return best; }
};

// Frames needed in flight to cover one round trip at this frame rate.
int WindowForRtt(int64_t rttUs, int fps, int maxInflight)
{
    if (rttUs == INT64_MAX) return maxInflight;
    const int64_t interval = 1000000 / std::max(fps, 1);
    const int64_t frames = (rttUs + interval - 1) / interval + 1;
    return (int)std::clamp<int64_t>(frames, kMinWindow, maxInflight);
}

// ---------------------------------------------------------------- logging

// Optional diagnostics file (DRAWSPLAY_MEDIA_LOG): the host only reports a
// helper's stderr when it fails, so tuning runs read stats from here.
FILE* g_logFile = [] {
    char path[MAX_PATH] = {};
    size_t n = 0;
    if (getenv_s(&n, path, sizeof path, "DRAWSPLAY_MEDIA_LOG") != 0 || n == 0) return (FILE*)nullptr;
    // Shared so the log can be read while the helper runs.
    return _fsopen(path, "a", _SH_DENYNO);
}();

void Log(const char* fmt, ...)
{
    char buf[1024];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(buf, sizeof buf, fmt, ap);
    va_end(ap);
    fprintf(stderr, "drawsplay-media-win: %s\n", buf);
    fflush(stderr);
    if (g_logFile) {
        SYSTEMTIME t;
        GetLocalTime(&t);
        fprintf(g_logFile, "%02d:%02d:%02d.%03d %s\n", t.wHour, t.wMinute, t.wSecond, t.wMilliseconds, buf);
        fflush(g_logFile);
    }
}

int64_t NowUs()
{
    static LARGE_INTEGER freq = [] { LARGE_INTEGER f; QueryPerformanceFrequency(&f); return f; }();
    LARGE_INTEGER c;
    QueryPerformanceCounter(&c);
    return (int64_t)(c.QuadPart * 1000000 / freq.QuadPart);
}

// ---------------------------------------------------------------- options

struct Options {
    bool probe = false;
    bool selftest = false;
    unsigned monitorId = 0;
    bool haveMonitor = false;
    int fps = 60;
    int kbps = 20000;
    int gop = 0; // 0 = 2 s
    int maxInflight = 0;
    int idleRefresh = 2;
    bool intraRefresh = true;
    bool keyframesOnDemand = false;
    bool cursorOverlay = false;
};

bool ParseOptions(int argc, char** argv, Options& o)
{
    for (int i = 1; i < argc; ++i) {
        std::string a = argv[i];
        auto next = [&](int& out) -> bool {
            if (i + 1 >= argc) return false;
            out = atoi(argv[++i]);
            return true;
        };
        if (a == "--probe") {
            o.probe = true;
        } else if (a == "--selftest") {
            o.selftest = true;
        } else if (a == "--monitor-id") {
            int v;
            if (!next(v) || v < 0) return false;
            o.monitorId = (unsigned)v;
            o.haveMonitor = true;
        } else if (a == "--fps") {
            if (!next(o.fps)) return false;
        } else if (a == "--bitrate-kbps") {
            if (!next(o.kbps)) return false;
        } else if (a == "--gop") {
            if (!next(o.gop)) return false;
        } else if (a == "--max-inflight") {
            if (!next(o.maxInflight)) return false;
        } else if (a == "--idle-refresh") {
            if (!next(o.idleRefresh)) return false;
        } else if (a == "--intra-refresh" || a == "--cursor" || a == "--keyframes") {
            if (i + 1 >= argc) return false;
            std::string v = argv[++i];
            if (a == "--intra-refresh" && (v == "on" || v == "off")) o.intraRefresh = v == "on";
            else if (a == "--cursor" && (v == "overlay" || v == "off")) o.cursorOverlay = v == "overlay";
            else if (a == "--keyframes" && (v == "on-demand" || v == "periodic")) o.keyframesOnDemand = v == "on-demand";
            else return false;
        } else {
            Log("unknown option %s", a.c_str());
            return false;
        }
    }
    if (o.fps < 1 || o.fps > 240) o.fps = 60;
    if (o.kbps < 100) o.kbps = 100;
    if (o.gop <= 0) o.gop = o.fps * 2;
    if (o.maxInflight < 0) o.maxInflight = 0;
    if (o.idleRefresh < 0) o.idleRefresh = 0;
    return o.probe || o.selftest || o.haveMonitor;
}

// ---------------------------------------------------------------- stdin commands

// Wakes the capture loop early: an ack opened the window or the encoder
// wants input. The loop otherwise sleeps ~1 ms between frame checks.
HANDLE g_wake = CreateEventW(nullptr, FALSE, FALSE, nullptr);

struct Commands {
    std::atomic<uint32_t> kickSeq{0};
    std::atomic<int64_t> acked{0};
    std::atomic<bool> acksSeen{false};
    std::atomic<bool> forceKeyframe{false};
    std::atomic<int> newKbps{0};
    std::atomic<bool> quit{false};
} g_cmd;

void ParseCommand(const char* line)
{
    while (*line == ' ') ++line;
    switch (line[0]) {
    case 'K':
        g_cmd.kickSeq.store((uint32_t)strtoul(line + 1, nullptr, 10));
        break;
    case 'A': {
        long long v = strtoll(line + 1, nullptr, 10);
        if (v >= 0) {
            // Cumulative; never move backwards.
            int64_t cur = g_cmd.acked.load();
            while (v > cur && !g_cmd.acked.compare_exchange_weak(cur, v)) {}
            g_cmd.acksSeen.store(true);
            SetEvent(g_wake);
        }
        break;
    }
    case 'I':
        g_cmd.forceKeyframe.store(true);
        break;
    case 'B': {
        long v = strtol(line + 1, nullptr, 10);
        if (v > 0) g_cmd.newKbps.store((int)v);
        break;
    }
    case 'Q':
        g_cmd.quit.store(true);
        break;
    default:
        break;
    }
}

void StdinThread()
{
    HANDLE in = GetStdHandle(STD_INPUT_HANDLE);
    std::string pending;
    char buf[512];
    for (;;) {
        DWORD n = 0;
        if (!ReadFile(in, buf, sizeof buf, &n, nullptr) || n == 0) break;
        pending.append(buf, n);
        size_t nl;
        while ((nl = pending.find('\n')) != std::string::npos) {
            std::string line = pending.substr(0, nl);
            if (!line.empty() && line.back() == '\r') line.pop_back();
            ParseCommand(line.c_str());
            pending.erase(0, nl + 1);
        }
        if (pending.size() > 4096) pending.clear();
    }
    g_cmd.quit.store(true);
}

// ---------------------------------------------------------------- framed stdout

std::mutex g_outMu;
// Longest stdout write since the last stats line: a slow reader (the host
// blocked on the USB link) back-pressures the encoder through this pipe.
std::atomic<int64_t> g_maxWriteUs{0};

void WriteAll(HANDLE h, const void* p, DWORD n)
{
    const uint8_t* b = static_cast<const uint8_t*>(p);
    while (n) {
        DWORD w = 0;
        if (!WriteFile(h, b, n, &w, nullptr)) {
            // The host closed the pipe: the session is over.
            ExitProcess(0);
        }
        b += w;
        n -= w;
    }
}

void Put32(uint8_t* p, uint32_t v)
{
    p[0] = (uint8_t)v; p[1] = (uint8_t)(v >> 8); p[2] = (uint8_t)(v >> 16); p[3] = (uint8_t)(v >> 24);
}

void SendFrame(const uint8_t* data, size_t len, uint8_t kind, uint8_t flags, uint32_t seq, uint32_t pipeUs)
{
    uint8_t hdr[16] = {};
    Put32(hdr, (uint32_t)len);
    hdr[4] = kind;
    hdr[5] = flags;
    Put32(hdr + 8, seq);
    Put32(hdr + 12, pipeUs);
    std::lock_guard<std::mutex> lock(g_outMu);
    HANDLE out = GetStdHandle(STD_OUTPUT_HANDLE);
    const int64_t t0 = NowUs();
    WriteAll(out, hdr, sizeof hdr);
    WriteAll(out, data, (DWORD)len);
    const int64_t took = NowUs() - t0;
    int64_t prev = g_maxWriteUs.load();
    while (took > prev && !g_maxWriteUs.compare_exchange_weak(prev, took)) {}
}

// ---------------------------------------------------------------- driver access

HANDLE OpenIddDevice()
{
    ULONG size = 0;
    if (CM_Get_Device_Interface_List_SizeW(&size, (LPGUID)&GUID_DEVINTERFACE_DRAWSPLAY_IDD, nullptr,
            CM_GET_DEVICE_INTERFACE_LIST_PRESENT) != CR_SUCCESS || size <= 1) {
        return INVALID_HANDLE_VALUE;
    }
    std::vector<wchar_t> list(size);
    if (CM_Get_Device_Interface_ListW((LPGUID)&GUID_DEVINTERFACE_DRAWSPLAY_IDD, nullptr, list.data(), size,
            CM_GET_DEVICE_INTERFACE_LIST_PRESENT) != CR_SUCCESS) {
        return INVALID_HANDLE_VALUE;
    }
    return CreateFileW(list.data(), GENERIC_READ | GENERIC_WRITE, FILE_SHARE_READ | FILE_SHARE_WRITE,
        nullptr, OPEN_EXISTING, 0, nullptr);
}

bool QuerySurface(HANDLE dev, unsigned monitorId, DRAWSPLAY_GET_FRAME_SURFACE_OUT& out, unsigned flags = 0)
{
    DRAWSPLAY_GET_FRAME_SURFACE_IN in = { monitorId, flags };
    // METHOD_BUFFERED shares one buffer for input and output.
    union { DRAWSPLAY_GET_FRAME_SURFACE_IN in; DRAWSPLAY_GET_FRAME_SURFACE_OUT out; } io = {};
    io.in = in;
    DWORD got = 0;
    if (!DeviceIoControl(dev, IOCTL_DRAWSPLAY_GET_FRAME_SURFACE, &io, sizeof io.in, &io, sizeof io.out, &got, nullptr) ||
        got < sizeof io.out) {
        return false;
    }
    out = io.out;
    return true;
}

// ---------------------------------------------------------------- cursor

void PutBE32(std::vector<uint8_t>& b, uint32_t v)
{
    b.push_back((uint8_t)(v >> 24)); b.push_back((uint8_t)(v >> 16));
    b.push_back((uint8_t)(v >> 8)); b.push_back((uint8_t)v);
}

void PutBE16(std::vector<uint8_t>& b, uint32_t v)
{
    b.push_back((uint8_t)(v >> 8)); b.push_back((uint8_t)v);
}

// Reads the driver's shared cursor block (Public.h) and turns changes into
// cursor frames for the tablet.
struct CursorRelay {
    HANDLE mapping = nullptr;
    const DRAWSPLAY_CURSOR_SHARED* shared = nullptr;
    unsigned monitorId = 0;
    bool sentAny = false;
    bool lastVisible = false;
    int lastX = 0, lastY = 0;
    uint32_t lastShapeId = 0;
    std::vector<uint8_t> shape;

    bool Open()
    {
        if (shared) return true;
        wchar_t name[96];
        swprintf_s(name, DRAWSPLAY_CURSOR_NAME_FORMAT, monitorId);
        mapping = OpenFileMappingW(FILE_MAP_READ, FALSE, name);
        if (!mapping) return false;
        shared = static_cast<const DRAWSPLAY_CURSOR_SHARED*>(
            MapViewOfFile(mapping, FILE_MAP_READ, 0, 0, sizeof(DRAWSPLAY_CURSOR_SHARED)));
        if (!shared) {
            CloseHandle(mapping);
            mapping = nullptr;
            return false;
        }
        return true;
    }

    // Sends the cursor when it moved, changed shape or visibility.
    // hardwareCursor is the driver's flag: without it the OS draws the
    // cursor into the frames, so the overlay must be hidden.
    void Poll(bool hardwareCursor)
    {
        if (!Open()) return;
        bool visible = false;
        int x = 0, y = 0;
        uint32_t shapeId = 0, w = 0, h = 0, hx = 0, hy = 0;
        bool haveShape = false;
        for (int tries = 0;; ++tries) {
            if (tries == 16) return; // the driver is mid-write; next poll
            const LONG s1 = shared->Seq;
            if (s1 & 1) { YieldProcessor(); continue; }
            MemoryBarrier();
            visible = shared->Visible != 0;
            x = shared->X;
            y = shared->Y;
            shapeId = shared->ShapeId;
            w = std::min<uint32_t>(shared->Width, DRAWSPLAY_CURSOR_MAX);
            h = std::min<uint32_t>(shared->Height, DRAWSPLAY_CURSOR_MAX);
            hx = shared->XHot;
            hy = shared->YHot;
            haveShape = w > 0 && h > 0 && (!sentAny || shapeId != lastShapeId);
            if (haveShape) shape.assign(shared->Shape, shared->Shape + (size_t)w * h * 4);
            MemoryBarrier();
            if (shared->Seq == s1) break;
        }
        visible = visible && hardwareCursor && w > 0 && h > 0;
        if (sentAny && visible == lastVisible && (!visible || (x == lastX && y == lastY && shapeId == lastShapeId))) {
            return;
        }
        std::vector<uint8_t> p;
        p.reserve(13 + (haveShape ? 8 + shape.size() : 0));
        p.push_back((uint8_t)((visible ? 1 : 0) | (haveShape ? 2 : 0)));
        PutBE32(p, (uint32_t)x);
        PutBE32(p, (uint32_t)y);
        PutBE32(p, shapeId);
        if (haveShape) {
            PutBE16(p, w); PutBE16(p, h); PutBE16(p, hx); PutBE16(p, hy);
            p.insert(p.end(), shape.begin(), shape.end());
            lastShapeId = shapeId;
        }
        SendFrame(p.data(), p.size(), kKindCursor, 0, g_cmd.kickSeq.load(), 0);
        sentAny = true;
        lastVisible = visible;
        lastX = x;
        lastY = y;
    }
};

// A desktop that is not changing presents no frames, so a freshly enabled
// export would wait for one indefinitely. Repainting every window makes the
// compositor present the virtual monitor once.
void NudgeDesktopRepaint()
{
    RedrawWindow(nullptr, nullptr, nullptr, RDW_INVALIDATE | RDW_ALLCHILDREN | RDW_ERASE | RDW_FRAME);
}

// ---------------------------------------------------------------- D3D11

struct Gpu {
    ComPtr<IDXGIAdapter1> adapter;
    ComPtr<ID3D11Device> device;
    ComPtr<ID3D11Device1> device1;
    ComPtr<ID3D11DeviceContext> context;
    ComPtr<ID3D11VideoDevice> videoDevice;
    ComPtr<ID3D11VideoContext> videoContext;
    std::string description;
};

HRESULT CreateGpu(IDXGIAdapter1* adapter, Gpu& g)
{
    g.adapter = adapter;
    DXGI_ADAPTER_DESC1 desc = {};
    adapter->GetDesc1(&desc);
    char name[256] = {};
    WideCharToMultiByte(CP_UTF8, 0, desc.Description, -1, name, sizeof name - 1, nullptr, nullptr);
    g.description = name;

    static const D3D_FEATURE_LEVEL levels[] = {
        D3D_FEATURE_LEVEL_11_1, D3D_FEATURE_LEVEL_11_0, D3D_FEATURE_LEVEL_10_1, D3D_FEATURE_LEVEL_10_0,
    };
    UINT flags = D3D11_CREATE_DEVICE_BGRA_SUPPORT | D3D11_CREATE_DEVICE_VIDEO_SUPPORT;
    HRESULT hr = D3D11CreateDevice(adapter, D3D_DRIVER_TYPE_UNKNOWN, nullptr, flags, levels, ARRAYSIZE(levels),
        D3D11_SDK_VERSION, &g.device, nullptr, &g.context);
    if (hr == E_INVALIDARG) {
        hr = D3D11CreateDevice(adapter, D3D_DRIVER_TYPE_UNKNOWN, nullptr, flags, levels + 1, ARRAYSIZE(levels) - 1,
            D3D11_SDK_VERSION, &g.device, nullptr, &g.context);
    }
    if (FAILED(hr)) return hr;
    // The asynchronous encoder MFT uses the device from its own threads.
    ComPtr<ID3D10Multithread> mt;
    if (SUCCEEDED(g.device.As(&mt))) mt->SetMultithreadProtected(TRUE);
    if (FAILED(hr = g.device.As(&g.device1))) return hr;
    if (FAILED(hr = g.device.As(&g.videoDevice))) return hr;
    if (FAILED(hr = g.context.As(&g.videoContext))) return hr;
    return S_OK;
}

HRESULT CreateGpuForLuid(LUID luid, Gpu& g)
{
    ComPtr<IDXGIFactory4> factory;
    HRESULT hr = CreateDXGIFactory1(IID_PPV_ARGS(&factory));
    if (FAILED(hr)) return hr;
    ComPtr<IDXGIAdapter1> adapter;
    if (FAILED(hr = factory->EnumAdapterByLuid(luid, IID_PPV_ARGS(&adapter)))) return hr;
    return CreateGpu(adapter.Get(), g);
}

// BGRA -> NV12 on the GPU (BT.709, limited range: what the host's other
// pipelines produce and declare in the SPS).
struct Converter {
    ComPtr<ID3D11VideoProcessorEnumerator> enumerator;
    ComPtr<ID3D11VideoProcessor> processor;
    ComPtr<ID3D11VideoProcessorInputView> inputView;
    std::map<std::pair<ID3D11Texture2D*, UINT>, ComPtr<ID3D11VideoProcessorOutputView>> outputViews;

    HRESULT Init(Gpu& g, ID3D11Texture2D* source, UINT inW, UINT inH, UINT outW, UINT outH, int fps)
    {
        D3D11_VIDEO_PROCESSOR_CONTENT_DESC cd = {};
        cd.InputFrameFormat = D3D11_VIDEO_FRAME_FORMAT_PROGRESSIVE;
        cd.InputFrameRate = { (UINT)fps, 1 };
        cd.InputWidth = inW;
        cd.InputHeight = inH;
        cd.OutputFrameRate = { (UINT)fps, 1 };
        cd.OutputWidth = outW;
        cd.OutputHeight = outH;
        cd.Usage = D3D11_VIDEO_USAGE_OPTIMAL_SPEED;
        HRESULT hr = g.videoDevice->CreateVideoProcessorEnumerator(&cd, &enumerator);
        if (FAILED(hr)) return hr;
        UINT support = 0;
        if (FAILED(enumerator->CheckVideoProcessorFormat(DXGI_FORMAT_NV12, &support)) ||
            !(support & D3D11_VIDEO_PROCESSOR_FORMAT_SUPPORT_OUTPUT)) {
            return E_NOTIMPL;
        }
        if (FAILED(hr = g.videoDevice->CreateVideoProcessor(enumerator.Get(), 0, &processor))) return hr;

        D3D11_VIDEO_PROCESSOR_INPUT_VIEW_DESC iv = {};
        iv.ViewDimension = D3D11_VPIV_DIMENSION_TEXTURE2D;
        if (FAILED(hr = g.videoDevice->CreateVideoProcessorInputView(source, enumerator.Get(), &iv, &inputView))) return hr;

        ComPtr<ID3D11VideoContext1> vc1;
        if (SUCCEEDED(g.videoContext.As(&vc1))) {
            vc1->VideoProcessorSetStreamColorSpace1(processor.Get(), 0, DXGI_COLOR_SPACE_RGB_FULL_G22_NONE_P709);
            vc1->VideoProcessorSetOutputColorSpace1(processor.Get(), DXGI_COLOR_SPACE_YCBCR_STUDIO_G22_LEFT_P709);
        } else {
            D3D11_VIDEO_PROCESSOR_COLOR_SPACE in = {};
            in.RGB_Range = 0; // full
            D3D11_VIDEO_PROCESSOR_COLOR_SPACE out = {};
            out.YCbCr_Matrix = 1; // BT.709
            out.Nominal_Range = D3D11_VIDEO_PROCESSOR_NOMINAL_RANGE_16_235;
            g.videoContext->VideoProcessorSetStreamColorSpace(processor.Get(), 0, &in);
            g.videoContext->VideoProcessorSetOutputColorSpace(processor.Get(), &out);
        }
        g.videoContext->VideoProcessorSetStreamFrameFormat(processor.Get(), 0, D3D11_VIDEO_FRAME_FORMAT_PROGRESSIVE);
        g.videoContext->VideoProcessorSetStreamAutoProcessingMode(processor.Get(), 0, FALSE);
        RECT src = { 0, 0, (LONG)inW, (LONG)inH };
        RECT dst = { 0, 0, (LONG)outW, (LONG)outH };
        g.videoContext->VideoProcessorSetStreamSourceRect(processor.Get(), 0, TRUE, &src);
        g.videoContext->VideoProcessorSetStreamDestRect(processor.Get(), 0, TRUE, &dst);
        g.videoContext->VideoProcessorSetOutputTargetRect(processor.Get(), TRUE, &dst);
        return S_OK;
    }

    HRESULT Convert(Gpu& g, ID3D11Texture2D* target, UINT subresource)
    {
        auto key = std::make_pair(target, subresource);
        auto it = outputViews.find(key);
        if (it == outputViews.end()) {
            D3D11_TEXTURE2D_DESC td = {};
            target->GetDesc(&td);
            D3D11_VIDEO_PROCESSOR_OUTPUT_VIEW_DESC ov = {};
            if (td.ArraySize > 1) {
                ov.ViewDimension = D3D11_VPOV_DIMENSION_TEXTURE2DARRAY;
                ov.Texture2DArray.FirstArraySlice = subresource;
                ov.Texture2DArray.ArraySize = 1;
            } else {
                ov.ViewDimension = D3D11_VPOV_DIMENSION_TEXTURE2D;
            }
            ComPtr<ID3D11VideoProcessorOutputView> view;
            HRESULT hr = g.videoDevice->CreateVideoProcessorOutputView(target, enumerator.Get(), &ov, &view);
            if (FAILED(hr)) return hr;
            // The sample pool is small and fixed; the map stays bounded.
            it = outputViews.emplace(key, view).first;
        }
        D3D11_VIDEO_PROCESSOR_STREAM stream = {};
        stream.Enable = TRUE;
        stream.pInputSurface = inputView.Get();
        return g.videoContext->VideoProcessorBlt(processor.Get(), it->second.Get(), 0, 1, &stream);
    }
};

// ---------------------------------------------------------------- encoder

void SetCodecValue(ICodecAPI* api, const GUID& key, VARTYPE vt, ULONGLONG value, const char* name)
{
    VARIANT v;
    VariantInit(&v);
    v.vt = vt;
    if (vt == VT_BOOL) v.boolVal = value ? VARIANT_TRUE : VARIANT_FALSE;
    else if (vt == VT_UI8) v.ullVal = value;
    else v.ulVal = (ULONG)value;
    HRESULT hr = api->SetValue(&key, &v);
    if (FAILED(hr)) Log("encoder ignores %s (0x%08lx)", name, (unsigned long)hr);
}

struct FrameMeta {
    int64_t sampleTime;
    int64_t captureUs;
    uint32_t seq;
};

struct Encoder {
    ComPtr<IMFTransform> mft;
    ComPtr<ICodecAPI> codecApi;
    ComPtr<IMFMediaEventGenerator> events;
    ComPtr<IMFDXGIDeviceManager> deviceManager;
    ComPtr<IMFVideoSampleAllocatorEx> allocator;
    std::string name;
    bool async = false;
    DWORD inId = 0, outId = 0;
    UINT width = 0, height = 0;
    int fps = 60;

    std::atomic<int> needInput{0};
    std::atomic<int64_t> outputs{0};
    std::atomic<bool> failed{false};
    std::mutex metaMu;
    std::deque<FrameMeta> meta;
    std::vector<uint8_t> sequenceHeader;
    bool sentFirst = false;
    bool logPipe = false; // selftest: log each frame's submit->output time

    // Set when the encoder accepted gradual intra refresh: the stream then
    // heals continuously and needs no periodic keyframes.
    bool intraRefresh = false;
    // Set before Configure: no periodic keyframes, only on request.
    bool keyframesOnDemand = false;

    HRESULT Configure(IMFActivate* activate, Gpu& g, UINT w, UINT h, int fpsIn, int kbps, int gop, int poolSize,
        bool wantIntraRefresh)
    {
        width = w;
        height = h;
        fps = fpsIn;
        HRESULT hr = activate->ActivateObject(IID_PPV_ARGS(&mft));
        if (FAILED(hr)) return hr;
        WCHAR* friendly = nullptr;
        UINT32 len = 0;
        if (SUCCEEDED(activate->GetAllocatedString(MFT_FRIENDLY_NAME_Attribute, &friendly, &len))) {
            char buf[256] = {};
            WideCharToMultiByte(CP_UTF8, 0, friendly, -1, buf, sizeof buf - 1, nullptr, nullptr);
            name = buf;
            CoTaskMemFree(friendly);
        }

        ComPtr<IMFAttributes> attrs;
        if (SUCCEEDED(mft->GetAttributes(&attrs))) {
            UINT32 isAsync = 0;
            if (SUCCEEDED(attrs->GetUINT32(MF_TRANSFORM_ASYNC, &isAsync)) && isAsync) {
                async = true;
                attrs->SetUINT32(MF_TRANSFORM_ASYNC_UNLOCK, TRUE);
            }
            attrs->SetUINT32(MF_LOW_LATENCY, TRUE);
        }

        UINT token = 0;
        if (FAILED(hr = MFCreateDXGIDeviceManager(&token, &deviceManager))) return hr;
        if (FAILED(hr = deviceManager->ResetDevice(g.device.Get(), token))) return hr;
        if (FAILED(hr = mft->ProcessMessage(MFT_MESSAGE_SET_D3D_MANAGER, (ULONG_PTR)deviceManager.Get()))) return hr;

        if (SUCCEEDED(mft.As(&codecApi))) {
            SetCodecValue(codecApi.Get(), CODECAPI_AVLowLatencyMode, VT_BOOL, 1, "low-latency mode");
            SetCodecValue(codecApi.Get(), CODECAPI_AVEncCommonRateControlMode, VT_UI4,
                eAVEncCommonRateControlMode_CBR, "CBR");
            SetCodecValue(codecApi.Get(), CODECAPI_AVEncCommonMeanBitRate, VT_UI4, (ULONGLONG)kbps * 1000, "bitrate");
            // 125 ms of stream, as the host's ffmpeg pipelines use: keyframes
            // keep their quality while the burst on the link stays bounded.
            SetCodecValue(codecApi.Get(), CODECAPI_AVEncCommonBufferSize, VT_UI4,
                (ULONGLONG)std::max(kbps / 8, 100) * 1000, "VBV size");
            // Periodic keyframes are several times the size of a normal
            // frame, so each one is a latency spike on the link. With
            // gradual intra refresh every frame carries a slice of intra
            // blocks instead (one full wave per second) and keyframes are
            // sent only when the tablet asks for one ("I").
            // Without intra refresh, keyframes on request alone are still
            // safe on a lossless link (USB, TCP) as long as the tablet asks
            // whenever it drops pictures; the host selects that mode
            // (--keyframes on-demand) only for tablets that do.
            intraRefresh = false;
            if (wantIntraRefresh) {
                // Some encoders accept the property without advertising it.
                VARIANT v;
                VariantInit(&v);
                v.vt = VT_UI4;
                v.ulVal = (ULONG)fps;
                intraRefresh = SUCCEEDED(codecApi->SetValue(&CODECAPI_AVEncVideoGradualIntraRefresh, &v));
            }
            const ULONGLONG gopFrames = (intraRefresh || keyframesOnDemand) ? (ULONGLONG)fps * 3600 : (ULONGLONG)gop;
            SetCodecValue(codecApi.Get(), CODECAPI_AVEncMPVGOPSize, VT_UI4, gopFrames, "GOP size");
            SetCodecValue(codecApi.Get(), CODECAPI_AVEncMPVDefaultBPictureCount, VT_UI4, 0, "no B-frames");
        }

        hr = mft->GetStreamIDs(1, &inId, 1, &outId);
        if (hr == E_NOTIMPL) { inId = 0; outId = 0; hr = S_OK; }
        if (FAILED(hr)) return hr;

        // Output type first: encoders derive their input types from it.
        static const UINT32 profiles[] = { eAVEncH264VProfile_High, eAVEncH264VProfile_Main };
        hr = E_FAIL;
        for (UINT32 profile : profiles) {
            ComPtr<IMFMediaType> out;
            MFCreateMediaType(&out);
            out->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
            out->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_H264);
            out->SetUINT32(MF_MT_AVG_BITRATE, (UINT32)kbps * 1000);
            MFSetAttributeSize(out.Get(), MF_MT_FRAME_SIZE, w, h);
            MFSetAttributeRatio(out.Get(), MF_MT_FRAME_RATE, (UINT32)fps, 1);
            MFSetAttributeRatio(out.Get(), MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
            out->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
            out->SetUINT32(MF_MT_MPEG2_PROFILE, profile);
            hr = mft->SetOutputType(outId, out.Get(), 0);
            if (SUCCEEDED(hr)) break;
        }
        if (FAILED(hr)) return hr;

        ComPtr<IMFMediaType> in;
        MFCreateMediaType(&in);
        in->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
        in->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_NV12);
        MFSetAttributeSize(in.Get(), MF_MT_FRAME_SIZE, w, h);
        MFSetAttributeRatio(in.Get(), MF_MT_FRAME_RATE, (UINT32)fps, 1);
        in->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);
        if (FAILED(hr = mft->SetInputType(inId, in.Get(), 0))) return hr;

        // GPU input pool: render targets for the video processor that the
        // encoder reads directly (no copy).
        if (FAILED(hr = MFCreateVideoSampleAllocatorEx(IID_PPV_ARGS(&allocator)))) return hr;
        if (FAILED(hr = allocator->SetDirectXManager(deviceManager.Get()))) return hr;
        ComPtr<IMFAttributes> poolAttrs;
        MFCreateAttributes(&poolAttrs, 2);
        poolAttrs->SetUINT32(MF_SA_D3D11_BINDFLAGS, D3D11_BIND_RENDER_TARGET);
        poolAttrs->SetUINT32(MF_SA_D3D11_USAGE, D3D11_USAGE_DEFAULT);
        if (FAILED(hr = allocator->InitializeSampleAllocatorEx(2, (DWORD)poolSize, poolAttrs.Get(), in.Get()))) return hr;

        if (async && FAILED(hr = mft.As(&events))) return hr;
        mft->ProcessMessage(MFT_MESSAGE_COMMAND_FLUSH, 0);
        if (FAILED(hr = mft->ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0))) return hr;
        if (FAILED(hr = mft->ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0))) return hr;
        if (!async) needInput.store(1);

        ComPtr<IMFMediaType> cur;
        if (SUCCEEDED(mft->GetOutputCurrentType(outId, &cur))) LoadSequenceHeader(cur.Get());
        return S_OK;
    }

    void LoadSequenceHeader(IMFMediaType* type)
    {
        UINT32 size = 0;
        if (SUCCEEDED(type->GetBlobSize(MF_MT_MPEG_SEQUENCE_HEADER, &size)) && size > 0) {
            sequenceHeader.resize(size);
            type->GetBlob(MF_MT_MPEG_SEQUENCE_HEADER, sequenceHeader.data(), size, nullptr);
        }
    }

    void SetBitrate(int kbps)
    {
        if (codecApi) SetCodecValue(codecApi.Get(), CODECAPI_AVEncCommonMeanBitRate, VT_UI4, (ULONGLONG)kbps * 1000, "bitrate");
    }

    void ForceKeyframe()
    {
        if (codecApi) SetCodecValue(codecApi.Get(), CODECAPI_AVEncVideoForceKeyFrame, VT_UI4, 1, "force keyframe");
    }

    static bool HasSPS(const uint8_t* p, size_t n)
    {
        for (size_t i = 0; i + 3 < n; ++i) {
            if (p[i] == 0 && p[i + 1] == 0 && p[i + 2] == 1 && (p[i + 3] & 0x1f) == 7) return true;
        }
        return false;
    }

    // Pulls one output sample and writes it to stdout. Returns false when the
    // encoder has nothing more right now.
    bool DrainOne()
    {
        MFT_OUTPUT_STREAM_INFO info = {};
        if (FAILED(mft->GetOutputStreamInfo(outId, &info))) return false;
        const bool provides = (info.dwFlags & (MFT_OUTPUT_STREAM_PROVIDES_SAMPLES | MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES)) != 0;

        MFT_OUTPUT_DATA_BUFFER ob = {};
        ob.dwStreamID = outId;
        ComPtr<IMFSample> own;
        if (!provides) {
            ComPtr<IMFMediaBuffer> buf;
            DWORD cap = std::max<DWORD>(info.cbSize, width * height * 3 / 2);
            if (FAILED(MFCreateSample(&own)) || FAILED(MFCreateMemoryBuffer(cap, &buf))) return false;
            own->AddBuffer(buf.Get());
            ob.pSample = own.Get();
        }
        DWORD status = 0;
        HRESULT hr = mft->ProcessOutput(0, 1, &ob, &status);
        if (ob.pEvents) ob.pEvents->Release();
        if (hr == MF_E_TRANSFORM_NEED_MORE_INPUT) return false;
        if (hr == MF_E_TRANSFORM_STREAM_CHANGE) {
            ComPtr<IMFMediaType> t;
            if (SUCCEEDED(mft->GetOutputAvailableType(outId, 0, &t))) {
                mft->SetOutputType(outId, t.Get(), 0);
                LoadSequenceHeader(t.Get());
            }
            return true;
        }
        if (FAILED(hr)) {
            Log("ProcessOutput failed 0x%08lx", (unsigned long)hr);
            failed.store(true);
            return false;
        }
        ComPtr<IMFSample> sample;
        sample.Attach(ob.pSample);
        if (!provides) sample->AddRef(); // `own` keeps its reference too

        UINT32 clean = 0;
        sample->GetUINT32(MFSampleExtension_CleanPoint, &clean);
        LONGLONG time = 0;
        sample->GetSampleTime(&time);

        FrameMeta m = { time, NowUs(), g_cmd.kickSeq.load() };
        {
            std::lock_guard<std::mutex> lock(metaMu);
            // No B-frames: outputs come in input order.
            while (!meta.empty() && meta.front().sampleTime < time) meta.pop_front();
            if (!meta.empty()) {
                m = meta.front();
                meta.pop_front();
            }
        }

        ComPtr<IMFMediaBuffer> buffer;
        if (FAILED(sample->ConvertToContiguousBuffer(&buffer))) return true;
        BYTE* data = nullptr;
        DWORD size = 0;
        if (FAILED(buffer->Lock(&data, nullptr, &size))) return true;
        uint32_t pipeUs = (uint32_t)std::max<int64_t>(0, NowUs() - m.captureUs);
        if (logPipe) Log("frame %u: submit->output %.1f ms", m.seq, pipeUs / 1000.0);
        if (clean && !HasSPS(data, size) && !sequenceHeader.empty()) {
            // Some encoders put SPS/PPS only in the media type; the tablet
            // needs them in-band before every IDR.
            std::vector<uint8_t> joined(sequenceHeader);
            joined.insert(joined.end(), data, data + size);
            SendFrame(joined.data(), joined.size(), kKindAU, kFlagKeyframe, m.seq, pipeUs);
        } else {
            SendFrame(data, size, kKindAU, clean ? kFlagKeyframe : 0, m.seq, pipeUs);
        }
        buffer->Unlock();
        outputs.fetch_add(1);
        sentFirst = true;
        return true;
    }

    // Asynchronous MFTs report demand and output through events.
    void EventLoop()
    {
        for (;;) {
            ComPtr<IMFMediaEvent> ev;
            if (FAILED(events->GetEvent(0, &ev))) {
                failed.store(true);
                return;
            }
            MediaEventType type = MEUnknown;
            ev->GetType(&type);
            if (type == METransformNeedInput) {
                needInput.fetch_add(1);
                SetEvent(g_wake);
            } else if (type == METransformHaveOutput) {
                DrainOne();
            } else if (type == MEError) {
                HRESULT st = S_OK;
                ev->GetStatus(&st);
                Log("encoder error 0x%08lx", (unsigned long)st);
                failed.store(true);
                return;
            }
        }
    }

    HRESULT Submit(Gpu& g, Converter& conv, int64_t captureUs, uint32_t seq, bool keyframe)
    {
        ComPtr<IMFSample> sample;
        HRESULT hr = allocator->AllocateSample(&sample);
        if (FAILED(hr)) return hr; // MF_E_SAMPLEALLOCATOR_EMPTY: retry later
        ComPtr<IMFMediaBuffer> buf;
        ComPtr<IMFDXGIBuffer> dxgi;
        ComPtr<ID3D11Texture2D> tex;
        UINT sub = 0;
        if (FAILED(hr = sample->GetBufferByIndex(0, &buf))) return hr;
        if (FAILED(hr = buf.As(&dxgi))) return hr;
        if (FAILED(hr = dxgi->GetResource(IID_PPV_ARGS(&tex)))) return hr;
        dxgi->GetSubresourceIndex(&sub);
        if (FAILED(hr = conv.Convert(g, tex.Get(), sub))) return hr;

        const LONGLONG time = captureUs * 10; // 100 ns units
        sample->SetSampleTime(time);
        sample->SetSampleDuration(10000000LL / fps);
        if (keyframe) ForceKeyframe();
        {
            std::lock_guard<std::mutex> lock(metaMu);
            meta.push_back({ time, captureUs, seq });
            if (meta.size() > 64) meta.pop_front();
        }
        hr = mft->ProcessInput(inId, sample.Get(), 0);
        if (FAILED(hr)) return hr;
        if (async) {
            needInput.fetch_sub(1);
        } else {
            while (DrainOne()) {}
        }
        return S_OK;
    }
};

HRESULT CreateEncoder(Gpu& g, LUID luid, UINT w, UINT h, int fps, int kbps, int gop, int poolSize, Encoder& enc,
    bool intraRefresh = true, bool keyframesOnDemand = false)
{
    enc.keyframesOnDemand = keyframesOnDemand;
    MFT_REGISTER_TYPE_INFO inType = { MFMediaType_Video, MFVideoFormat_NV12 };
    MFT_REGISTER_TYPE_INFO outType = { MFMediaType_Video, MFVideoFormat_H264 };
    IMFActivate** activates = nullptr;
    UINT32 count = 0;
    const UINT32 flags = MFT_ENUM_FLAG_HARDWARE | MFT_ENUM_FLAG_SORTANDFILTER;

    // MFTEnum2 filters to encoders on this adapter (Windows 10 1703+).
    using MFTEnum2Fn = HRESULT(WINAPI*)(GUID, UINT32, const MFT_REGISTER_TYPE_INFO*, const MFT_REGISTER_TYPE_INFO*,
        IMFAttributes*, IMFActivate***, UINT32*);
    auto enum2 = reinterpret_cast<MFTEnum2Fn>(GetProcAddress(GetModuleHandleW(L"mfplat.dll"), "MFTEnum2"));
    HRESULT hr = E_FAIL;
    if (enum2) {
        ComPtr<IMFAttributes> a;
        MFCreateAttributes(&a, 1);
        a->SetBlob(MFT_ENUM_ADAPTER_LUID, reinterpret_cast<const UINT8*>(&luid), sizeof luid);
        hr = enum2(MFT_CATEGORY_VIDEO_ENCODER, flags, &inType, &outType, a.Get(), &activates, &count);
    }
    if (FAILED(hr) || count == 0) {
        hr = MFTEnumEx(MFT_CATEGORY_VIDEO_ENCODER, flags, &inType, &outType, &activates, &count);
    }
    if (FAILED(hr)) return hr;
    if (count == 0) return MF_E_TOPO_CODEC_NOT_FOUND;

    hr = MF_E_TOPO_CODEC_NOT_FOUND;
    for (UINT32 i = 0; i < count; ++i) {
        if (FAILED(hr)) {
            enc.mft.Reset();
            enc.codecApi.Reset();
            enc.allocator.Reset();
            enc.deviceManager.Reset();
            enc.async = false;
            hr = enc.Configure(activates[i], g, w, h, fps, kbps, gop, poolSize, intraRefresh);
            if (FAILED(hr)) {
                Log("encoder candidate %u rejected configuration (0x%08lx)", i, (unsigned long)hr);
                activates[i]->ShutdownObject();
            }
        }
        activates[i]->Release();
    }
    CoTaskMemFree(activates);
    return hr;
}

// ---------------------------------------------------------------- probe

int RunProbe(const Options& o)
{
    ComPtr<IDXGIFactory1> factory;
    if (FAILED(CreateDXGIFactory1(IID_PPV_ARGS(&factory)))) {
        printf("{\"ok\":false,\"reason\":\"no DXGI factory\",\"helperVersion\":%d}\n", kHelperVersion);
        return 0;
    }
    std::string reason = "no hardware H.264 encoder";
    for (UINT i = 0;; ++i) {
        ComPtr<IDXGIAdapter1> adapter;
        if (factory->EnumAdapters1(i, &adapter) == DXGI_ERROR_NOT_FOUND) break;
        DXGI_ADAPTER_DESC1 d = {};
        adapter->GetDesc1(&d);
        if (d.Flags & DXGI_ADAPTER_FLAG_SOFTWARE) continue;
        Gpu g;
        if (FAILED(CreateGpu(adapter.Get(), g))) continue;
        // A small render-target texture stands in for the driver's surface.
        D3D11_TEXTURE2D_DESC td = {};
        td.Width = 1280; td.Height = 720; td.MipLevels = 1; td.ArraySize = 1;
        td.Format = DXGI_FORMAT_B8G8R8A8_UNORM; td.SampleDesc.Count = 1;
        td.BindFlags = D3D11_BIND_RENDER_TARGET | D3D11_BIND_SHADER_RESOURCE;
        ComPtr<ID3D11Texture2D> tex;
        Converter conv;
        if (FAILED(g.device->CreateTexture2D(&td, nullptr, &tex)) ||
            FAILED(conv.Init(g, tex.Get(), 1280, 720, 1280, 720, o.fps))) {
            reason = "no GPU BGRA->NV12 conversion on " + g.description;
            continue;
        }
        Encoder enc;
        if (FAILED(CreateEncoder(g, d.AdapterLuid, 1280, 720, o.fps, 8000, o.fps * 2, 6, enc))) continue;
        std::string vendor = g.description + " / " + enc.name;
        for (char& c : vendor) if (c == '"' || c == '\\') c = ' ';
        printf("{\"ok\":true,\"vendor\":\"%s\",\"entrypoint\":\"mediafoundation\",\"profile\":\"h264\",\"helperVersion\":%d}\n",
            vendor.c_str(), kHelperVersion);
        return 0;
    }
    printf("{\"ok\":false,\"reason\":\"%s\",\"helperVersion\":%d}\n", reason.c_str(), kHelperVersion);
    return 0;
}

// ---------------------------------------------------------------- selftest

// Encodes animated frames through the real conversion/encoder/output path
// without the display driver, so the helper can be verified on any machine
// with a GPU. stdout carries the same framed stream as a capture session.
int RunSelftest(const Options& o)
{
    ComPtr<IDXGIFactory1> factory;
    if (FAILED(CreateDXGIFactory1(IID_PPV_ARGS(&factory)))) return 1;
    for (UINT i = 0;; ++i) {
        ComPtr<IDXGIAdapter1> adapter;
        if (factory->EnumAdapters1(i, &adapter) == DXGI_ERROR_NOT_FOUND) break;
        DXGI_ADAPTER_DESC1 d = {};
        adapter->GetDesc1(&d);
        if (d.Flags & DXGI_ADAPTER_FLAG_SOFTWARE) continue;
        Gpu g;
        if (FAILED(CreateGpu(adapter.Get(), g))) continue;
        const UINT w = 1280, h = 720;
        D3D11_TEXTURE2D_DESC td = {};
        td.Width = w; td.Height = h; td.MipLevels = 1; td.ArraySize = 1;
        td.Format = DXGI_FORMAT_B8G8R8A8_UNORM; td.SampleDesc.Count = 1;
        td.BindFlags = D3D11_BIND_RENDER_TARGET | D3D11_BIND_SHADER_RESOURCE;
        ComPtr<ID3D11Texture2D> tex;
        ComPtr<ID3D11RenderTargetView> rtv;
        Converter conv;
        if (FAILED(g.device->CreateTexture2D(&td, nullptr, &tex)) ||
            FAILED(g.device->CreateRenderTargetView(tex.Get(), nullptr, &rtv)) ||
            FAILED(conv.Init(g, tex.Get(), w, h, w, h, o.fps))) {
            continue;
        }
        auto encoder = std::make_shared<Encoder>();
        Encoder& enc = *encoder;
        if (FAILED(CreateEncoder(g, d.AdapterLuid, w, h, o.fps, o.kbps, o.gop, 8, enc, o.intraRefresh,
                o.keyframesOnDemand))) continue;
        Log("selftest: %s on %s (%s, intra refresh %s, %s)", enc.name.c_str(), g.description.c_str(),
            enc.async ? "async" : "sync", enc.intraRefresh ? "on" : "unsupported/off",
            (enc.intraRefresh || o.keyframesOnDemand) ? "keyframes on request" : "periodic keyframes");
        // A blocked MFT worker must retain its state until ExitProcess.
        if (enc.async) std::thread([encoder] { encoder->EventLoop(); }).detach();

        // Long enough to span the periodic-keyframe interval (2 s at 60 fps).
        // DRAWSPLAY_SELFTEST_SPACING_MS paces submissions like a mostly
        // static desktop to expose encoders that hold frames back.
        const int frames = 300;
        int spacingMs = 0;
        {
            char v[16] = {};
            size_t n = 0;
            if (getenv_s(&n, v, sizeof v, "DRAWSPLAY_SELFTEST_SPACING_MS") == 0 && n > 0) spacingMs = atoi(v);
        }
        enc.logPipe = spacingMs > 0;
        int64_t start = NowUs();
        for (int f = 0; f < frames; ++f) {
            const float c[4] = { (f % 30) / 30.0f, 0.3f, 1.0f - (f % 30) / 30.0f, 1.0f };
            g.context->ClearRenderTargetView(rtv.Get(), c);
            int64_t waitUntil = NowUs() + 2000000;
            for (;;) {
                if (enc.failed.load()) { Log("selftest: encoder failed"); return 1; }
                if (enc.needInput.load() > 0) {
                    // Frame 0 opens the stream; the mid-stream request checks
                    // that "I" (keyframe on request) is honoured.
                    HRESULT hr = enc.Submit(g, conv, NowUs(), (uint32_t)f, f == 0 || f == frames / 2);
                    if (SUCCEEDED(hr)) break;
                    if (hr != MF_E_SAMPLEALLOCATOR_EMPTY && hr != MF_E_NOTACCEPTING) {
                        Log("selftest: submit failed 0x%08lx", (unsigned long)hr);
                        return 1;
                    }
                }
                if (NowUs() > waitUntil) { Log("selftest: encoder stopped accepting input"); return 1; }
                Sleep(1);
            }
            if (spacingMs > 0) {
                if (f == 20) break; // a handful of paced frames is enough
                Sleep((DWORD)spacingMs);
            }
        }
        if (spacingMs > 0) {
            Sleep(500);
            Log("selftest: paced run done, %lld outputs for 21 inputs", (long long)enc.outputs.load());
            return 0;
        }
        int64_t waitUntil = NowUs() + 2000000;
        while (enc.outputs.load() < frames && NowUs() < waitUntil) Sleep(2);
        Log("selftest: %lld/%d frames encoded in %lld ms",
            (long long)enc.outputs.load(), frames, (long long)((NowUs() - start) / 1000));
        return enc.outputs.load() >= frames - 2 ? 0 : 1; // an encoder may hold its last frame or two
    }
    Log("selftest: no usable GPU encoder");
    return 1;
}

// ---------------------------------------------------------------- capture

int RunCapture(const Options& o)
{
    HANDLE dev = OpenIddDevice();
    if (dev == INVALID_HANDLE_VALUE) {
        Log("DrawSplay display driver not found (needs the frame-export driver)");
        return 1;
    }

    // Enable export (and, for a tablet that draws the cursor itself, the
    // hardware cursor hand-off) and wait for the first published surface.
    const unsigned surfaceFlags = o.cursorOverlay ? DRAWSPLAY_FRAME_SURFACE_FLAG_HW_CURSOR : 0;
    DRAWSPLAY_GET_FRAME_SURFACE_OUT surf = {};
    const int64_t deadline = NowUs() + 4000000;
    int64_t nextNudge = 0;
    for (;;) {
        if (!QuerySurface(dev, o.monitorId, surf, surfaceFlags)) {
            Log("driver rejected frame export for monitor %u (old driver or monitor gone)", o.monitorId);
            return 1;
        }
        if (surf.Failed) {
            Log("driver could not create the shared frame surface");
            return 1;
        }
        if (surf.Generation != 0) break;
        if (NowUs() > deadline) {
            Log("no frame from the virtual display");
            return 1;
        }
        if (NowUs() >= nextNudge) {
            NudgeDesktopRepaint();
            nextNudge = NowUs() + 500000;
        }
        Sleep(10);
    }

    LUID luid = { surf.AdapterLuidLow, surf.AdapterLuidHigh };
    Gpu g;
    HRESULT hr = CreateGpuForLuid(luid, g);
    if (FAILED(hr)) {
        Log("cannot create a D3D11 device on the display's adapter (0x%08lx)", (unsigned long)hr);
        return 1;
    }

    ComPtr<ID3D11Texture2D> shared;
    ComPtr<IDXGIKeyedMutex> mutex;
    unsigned generation = 0;
    auto openShared = [&](unsigned gen) -> bool {
        wchar_t name[96];
        swprintf_s(name, DRAWSPLAY_FRAME_SURFACE_NAME_FORMAT, o.monitorId, gen);
        shared.Reset();
        mutex.Reset();
        HRESULT h = g.device1->OpenSharedResourceByName(name, DXGI_SHARED_RESOURCE_READ | DXGI_SHARED_RESOURCE_WRITE,
            IID_PPV_ARGS(&shared));
        if (FAILED(h) || FAILED(shared.As(&mutex))) {
            Log("cannot open the shared frame surface (0x%08lx)", (unsigned long)h);
            return false;
        }
        generation = gen;
        return true;
    };
    if (!openShared(surf.Generation)) return 1;

    const UINT srcW = surf.Width, srcH = surf.Height;
    const UINT encW = srcW & ~1u, encH = srcH & ~1u;

    // Private copy of the newest frame: the shared texture goes back to the
    // driver immediately, and this copy is what gets encoded whenever the
    // window allows (latest frame wins).
    D3D11_TEXTURE2D_DESC ld = {};
    shared->GetDesc(&ld);
    ld.MiscFlags = 0;
    ld.BindFlags = D3D11_BIND_RENDER_TARGET | D3D11_BIND_SHADER_RESOURCE;
    ComPtr<ID3D11Texture2D> latest;
    if (FAILED(hr = g.device->CreateTexture2D(&ld, nullptr, &latest))) {
        Log("cannot allocate the frame copy (0x%08lx)", (unsigned long)hr);
        return 1;
    }

    Converter conv;
    if (FAILED(hr = conv.Init(g, latest.Get(), srcW, srcH, encW, encH, o.fps))) {
        Log("GPU colour conversion unavailable (0x%08lx)", (unsigned long)hr);
        return 1;
    }
    auto encoder = std::make_shared<Encoder>();
    Encoder& enc = *encoder;
    const int pool = std::max(o.maxInflight, 4) + 4;
    if (FAILED(hr = CreateEncoder(g, luid, encW, encH, o.fps, o.kbps, o.gop, pool, enc, o.intraRefresh,
                   o.keyframesOnDemand))) {
        Log("no usable hardware H.264 encoder (0x%08lx)", (unsigned long)hr);
        return 1;
    }
    Log("encoding %ux%u @ %d fps, %d kbps with %s on %s (%s), max in flight %d, %s%s",
        encW, encH, o.fps, o.kbps, enc.name.c_str(), g.description.c_str(), enc.async ? "async" : "sync",
        o.maxInflight, enc.intraRefresh ? "intra refresh, " : "",
        (enc.intraRefresh || o.keyframesOnDemand) ? "keyframes on request" : "periodic keyframes");

    std::thread(StdinThread).detach();
    if (enc.async) std::thread([encoder] { encoder->EventLoop(); }).detach();

    std::vector<int64_t> sendUs(kSendRing, 0);
    WindowedMin rtt;
    // Recent round trips, for the stats line (median and 90th percentile).
    std::vector<int64_t> rttRecent(128, 0);
    size_t rttRecentCount = 0;
    const int64_t statsEveryUs = g_logFile ? 2000000 : 10000000;
    int window = o.maxInflight;
    bool dirty = false;
    int repeatsLeft = 0;
    int64_t captureUs = 0, lastSubmitUs = 0, nextSurfaceCheck = NowUs() + kSurfaceRecheckUs;
    uint32_t captureSeq = 0;
    int64_t submitted = 0, assumedThrough = 0, lastAcked = 0, fullSinceUs = 0;
    int64_t replacedWhileBlocked = 0, stallResets = 0, nextStatsUs = NowUs() + 10000000;
    bool blocked = false;
    bool first = true;
    int64_t waitWindow = 0, waitEncoderInput = 0, waitSamplePool = 0;
    CursorRelay cursor;
    cursor.monitorId = o.monitorId;
    bool hardwareCursor = surf.HardwareCursor != 0;

    while (!g_cmd.quit.load()) {
        if (enc.failed.load()) {
            Log("encoder failed");
            return 1;
        }
        if (int kbps = g_cmd.newKbps.exchange(0)) enc.SetBitrate(kbps);

        // 1. Take the newest frame from the driver, if there is one.
        // Never block inside AcquireSync: with a multithread-protected device
        // it can hold the device lock while waiting, starving the encoder's
        // own threads (which then rarely ask for input). Poll instead and
        // sleep below without the lock.
        hr = mutex->AcquireSync(1, 0);
        if (hr == S_OK || hr == static_cast<HRESULT>(WAIT_ABANDONED)) {
            g.context->CopyResource(latest.Get(), shared.Get());
            mutex->ReleaseSync(0);
            if (dirty && blocked) ++replacedWhileBlocked;
            dirty = true;
            captureUs = NowUs();
            captureSeq = g_cmd.kickSeq.load();
            repeatsLeft = o.idleRefresh;
        } else if (hr != static_cast<HRESULT>(WAIT_TIMEOUT)) {
            Log("shared surface lost (0x%08lx)", (unsigned long)hr);
            return 1;
        }

        const int64_t now = NowUs();
        if (now >= nextSurfaceCheck) {
            nextSurfaceCheck = now + kSurfaceRecheckUs;
            DRAWSPLAY_GET_FRAME_SURFACE_OUT s = {};
            if (!QuerySurface(dev, o.monitorId, s, surfaceFlags)) {
                Log("virtual display removed");
                return 0;
            }
            hardwareCursor = s.HardwareCursor != 0;
            if (s.Generation != 0 && s.Generation != generation) {
                if (s.Width != srcW || s.Height != srcH || s.AdapterLuidLow != luid.LowPart ||
                    s.AdapterLuidHigh != luid.HighPart) {
                    Log("display mode changed to %ux%u; restarting", s.Width, s.Height);
                    return 3;
                }
                if (!openShared(s.Generation)) return 1;
            }
        }

        // Cursor moves go out at once, independent of the video frames and
        // the frame window: they cost a few bytes, never an encode.
        // The stream must open with a keyframe, so cursor updates start only
        // after the first video frame is out (the first poll then sends the
        // full cursor state).
        if (o.cursorOverlay && enc.outputs.load() > 0) cursor.Poll(hardwareCursor);

        // 2. Frame window: at most `window` unacknowledged frames, where
        //    window covers one measured round trip (capped by maxInflight).
        bool windowOpen = true;
        if (o.maxInflight > 0 && g_cmd.acksSeen.load()) {
            const int64_t acked = g_cmd.acked.load();
            if (acked != lastAcked) {
                // Acks count this helper's frames in order, so the newest
                // acknowledged frame is number acked-1.
                const int64_t frame = acked - 1;
                if (acked > lastAcked && frame >= 0 && frame < submitted && submitted - frame <= (int64_t)kSendRing) {
                    const int64_t sample = now - sendUs[(size_t)frame % kSendRing];
                    rtt.Add(now, sample);
                    rttRecent[rttRecentCount++ % rttRecent.size()] = sample;
                    window = WindowForRtt(rtt.Get(), o.fps, o.maxInflight);
                }
                lastAcked = acked;
                fullSinceUs = 0; // progress: restart the stall clock
            }
            // Frames before assumedThrough were written off by the stall
            // guard; late acks for them simply catch up to this floor
            // instead of widening the window.
            int64_t inflight = submitted - std::max(acked, assumedThrough);
            if (inflight >= window) {
                // The stall clock runs only while the window is full, so an
                // idle pause (all frames acked, nothing in flight) can never
                // make the first full window of the next burst look stalled.
                if (fullSinceUs == 0) {
                    fullSinceUs = now;
                } else if (now - fullSinceUs > kAckStallUs) {
                    // Acks stopped (frames lost on a reconnect, or a client
                    // that stopped acking): write the in-flight frames off.
                    assumedThrough = submitted;
                    inflight = 0;
                    fullSinceUs = 0;
                    ++stallResets;
                }
            } else {
                fullSinceUs = 0;
            }
            windowOpen = inflight < window;
        }
        blocked = !windowOpen;

        // 3. Encode the newest frame when allowed.
        const bool keyframe = g_cmd.forceKeyframe.load();
        const bool refresh = !dirty && repeatsLeft > 0 && submitted > 0 && now - lastSubmitUs >= kIdleRefreshAfterUs;
        const bool want = dirty || refresh || (keyframe && submitted > 0);
        // Diagnostics: why a wanted encode did not happen this iteration.
        if (want && !windowOpen) ++waitWindow;
        else if (want && enc.needInput.load() <= 0) ++waitEncoderInput;
        if (want && windowOpen && enc.needInput.load() > 0) {
            hr = enc.Submit(g, conv, dirty ? captureUs : now, dirty ? captureSeq : g_cmd.kickSeq.load(),
                first || g_cmd.forceKeyframe.exchange(false));
            if (SUCCEEDED(hr)) {
                sendUs[(size_t)submitted % kSendRing] = now;
                ++submitted;
                lastSubmitUs = now;
                first = false;
                if (dirty) dirty = false;
                else if (repeatsLeft > 0) --repeatsLeft;
            } else if (hr == MF_E_SAMPLEALLOCATOR_EMPTY) {
                ++waitSamplePool;
            } else if (hr == MF_E_NOTACCEPTING) {
                ++waitEncoderInput;
            } else {
                Log("encode submit failed (0x%08lx)", (unsigned long)hr);
                return 1;
            }
        }

        if (now >= nextStatsUs) {
            nextStatsUs = now + statsEveryUs;
            DRAWSPLAY_GET_FRAME_SURFACE_OUT s = {};
            QuerySurface(dev, o.monitorId, s, surfaceFlags);
            auto ms = [](int64_t us) {
                return us == INT64_MAX ? std::string("n/a") : std::to_string(us / 1000) + "." + std::to_string(us / 100 % 10);
            };
            std::vector<int64_t> recent(rttRecent.begin(), rttRecent.begin() + std::min(rttRecentCount, rttRecent.size()));
            int64_t p50 = INT64_MAX, p90 = INT64_MAX;
            if (!recent.empty()) {
                std::sort(recent.begin(), recent.end());
                p50 = recent[recent.size() / 2];
                p90 = recent[recent.size() * 9 / 10];
            }
            Log("stats: driver published %u skipped %u; encoded %lld, out %lld, acked %lld, window %d, rtt min/p50/p90 %s/%s/%s ms, replaced-while-full %lld, ack stalls %lld; waits: window %lld, encoder input %lld, sample pool %lld; stdout write max %.1f ms",
                s.FramesPublished, s.FramesSkipped, (long long)submitted, (long long)enc.outputs.load(),
                (long long)std::max(g_cmd.acked.load(), assumedThrough), window,
                ms(rtt.Get()).c_str(), ms(p50).c_str(), ms(p90).c_str(),
                (long long)replacedWhileBlocked, (long long)stallResets,
                (long long)waitWindow, (long long)waitEncoderInput, (long long)waitSamplePool,
                g_maxWriteUs.exchange(0) / 1000.0);
        }
        // Sleep until an ack or encoder input request arrives, or ~1 ms.
        WaitForSingleObject(g_wake, kFramePollMs);
    }
    return 0;
}

} // namespace

int main(int argc, char** argv)
{
    Options o;
    if (!ParseOptions(argc, argv, o)) {
        fprintf(stderr,
            "usage: drawsplay-media-win --probe\n"
            "       drawsplay-media-win --selftest [--fps N] [--bitrate-kbps N] > frames.bin\n"
            "       drawsplay-media-win --monitor-id N [--fps N] [--bitrate-kbps N] [--gop N]\n"
            "                           [--max-inflight N] [--idle-refresh N] [--intra-refresh on|off]\n"
            "                           [--keyframes periodic|on-demand] [--cursor overlay|off]\n");
        return 2;
    }
    timeBeginPeriod(1); // 1 ms waits in the capture loop
    if (FAILED(CoInitializeEx(nullptr, COINIT_MULTITHREADED)) || FAILED(MFStartup(MF_VERSION, MFSTARTUP_LITE))) {
        Log("Media Foundation unavailable");
        return 1;
    }
    int rc = o.probe ? RunProbe(o) : o.selftest ? RunSelftest(o) : RunCapture(o);
    fflush(stdout);
    // Encoder threads may still be blocked in the MFT; exit without
    // unwinding them.
    ExitProcess((UINT)rc);
}
