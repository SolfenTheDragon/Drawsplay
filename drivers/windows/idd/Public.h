// Public.h - control protocol shared between the DrawSplay IddCx virtual
// display driver and the host.
//
// The host opens the driver's device interface and issues these buffered
// IOCTLs to plug and unplug virtual monitors. Each monitor starts at the
// requested mode (marked preferred) and also offers the tablet's native size
// plus standard 720p-4K resolutions in Windows Display Settings. All monitors created
// through a control handle are torn down automatically when that handle is
// closed, so a host crash never leaves an orphaned virtual display. Closing a
// handle that created no monitors (e.g. the capture helper's) changes nothing.
#pragma once

// {4f73af95-3814-4206-a651-125dfbb5e8f5}
// Device interface GUID the host uses to find and open the control device.
DEFINE_GUID(GUID_DEVINTERFACE_DRAWSPLAY_IDD,
    0x4f73af95, 0x3814, 0x4206, 0xa6, 0x51, 0x12, 0x5d, 0xfb, 0xb5, 0xe8, 0xf5);

#define DRAWSPLAY_IDD_IOCTL_BASE 0x800

#define IOCTL_DRAWSPLAY_ADD_MONITOR \
    CTL_CODE(FILE_DEVICE_UNKNOWN, DRAWSPLAY_IDD_IOCTL_BASE + 0, METHOD_BUFFERED, FILE_ANY_ACCESS)
#define IOCTL_DRAWSPLAY_REMOVE_MONITOR \
    CTL_CODE(FILE_DEVICE_UNKNOWN, DRAWSPLAY_IDD_IOCTL_BASE + 1, METHOD_BUFFERED, FILE_ANY_ACCESS)
#define IOCTL_DRAWSPLAY_REMOVE_ALL \
    CTL_CODE(FILE_DEVICE_UNKNOWN, DRAWSPLAY_IDD_IOCTL_BASE + 2, METHOD_BUFFERED, FILE_ANY_ACCESS)
// Frame export (v3). The first call for a monitor turns export on: from then
// on the driver copies every swap-chain frame into a shared, keyed-mutex
// texture that a capture process opens by name (see below). Any handle may
// query; querying never creates or removes monitors.
#define IOCTL_DRAWSPLAY_GET_FRAME_SURFACE \
    CTL_CODE(FILE_DEVICE_UNKNOWN, DRAWSPLAY_IDD_IOCTL_BASE + 3, METHOD_BUFFERED, FILE_ANY_ACCESS)

// Name of a monitor's shared frame texture, formatted with
// (MonitorId, Generation). Open it with
// ID3D11Device1::OpenSharedResourceByName on the adapter AdapterLuid names.
//
// Hand-off protocol (IDXGIKeyedMutex on the texture):
//   driver:   AcquireSync(0, 0)  -> CopyResource(frame) -> ReleaseSync(1)
//             (a timeout means the consumer still holds it: the frame is
//             skipped, so a slow consumer never stalls the desktop)
//   consumer: AcquireSync(1, t)  -> read/copy the frame -> ReleaseSync(0)
#define DRAWSPLAY_FRAME_SURFACE_NAME_FORMAT L"Global\\DrawSplayIddFrame-%u-%u"

#pragma pack(push, 1)

typedef struct _DRAWSPLAY_ADD_MONITOR_IN {
    unsigned int Width;        // active pixels of the initial (preferred) mode, e.g. 2960
    unsigned int Height;       // active pixels of the initial (preferred) mode, e.g. 1848
    unsigned int RefreshHz;    // preferred vertical refresh, e.g. 120
    // Added in v2 (optional; a 12-byte v1 request leaves both 0). The tablet's
    // native panel size, always offered in Windows Display Settings alongside
    // the standard 720p-4K modes even when the initial mode is smaller.
    // 0 = same as Width/Height.
    unsigned int NativeWidth;
    unsigned int NativeHeight;
} DRAWSPLAY_ADD_MONITOR_IN;

// Size of the original v1 request ({Width, Height, RefreshHz}).
#define DRAWSPLAY_ADD_MONITOR_IN_V1_SIZE 12

typedef struct _DRAWSPLAY_ADD_MONITOR_OUT {
    unsigned int MonitorId;  // opaque id used to remove this monitor later
} DRAWSPLAY_ADD_MONITOR_OUT;

typedef struct _DRAWSPLAY_REMOVE_MONITOR_IN {
    unsigned int MonitorId;
} DRAWSPLAY_REMOVE_MONITOR_IN;

typedef struct _DRAWSPLAY_GET_FRAME_SURFACE_IN {
    unsigned int MonitorId;
    // DRAWSPLAY_FRAME_SURFACE_FLAG_*; a 4-byte request (no Flags) means 0.
    unsigned int Flags;
} DRAWSPLAY_GET_FRAME_SURFACE_IN;

#define DRAWSPLAY_GET_FRAME_SURFACE_IN_V1_SIZE 4

// Hand the cursor to the consumer instead of drawing it into the frames:
// the driver enables the IddCx hardware cursor and publishes its position
// and shape in the shared DRAWSPLAY_CURSOR_SHARED block below. Only a
// consumer that shows the cursor itself may set this, because Windows then
// stops compositing it (cursors the driver cannot take, such as large or
// XOR/inverting ones, stay in the frames). Once enabled it stays enabled
// for the monitor's current swap chain.
#define DRAWSPLAY_FRAME_SURFACE_FLAG_HW_CURSOR 0x1

// Shared cursor state, a named file mapping formatted with (MonitorId).
// Seq is a sequence lock: odd while the driver writes. Readers copy the
// fields and retry if Seq was odd or changed. X/Y are the top-left pixel
// of the cursor image in monitor coordinates (may be negative). Shape is
// Width x Height 32-bit BGRA, straight alpha, rows packed (Width*4 bytes).
#define DRAWSPLAY_CURSOR_NAME_FORMAT L"Global\\DrawSplayIddCursor-%u"
#define DRAWSPLAY_CURSOR_MAX 128

typedef struct _DRAWSPLAY_CURSOR_SHARED {
    volatile long Seq;
    unsigned int Visible;
    int X;
    int Y;
    unsigned int ShapeId;  // changes whenever Shape changes
    unsigned int Width;
    unsigned int Height;
    unsigned int XHot;
    unsigned int YHot;
    unsigned char Shape[DRAWSPLAY_CURSOR_MAX * DRAWSPLAY_CURSOR_MAX * 4];
} DRAWSPLAY_CURSOR_SHARED;

typedef struct _DRAWSPLAY_GET_FRAME_SURFACE_OUT {
    // 0 while no surface exists yet (export just enabled, no swap chain, or
    // no frame presented since). Changes whenever the texture is recreated
    // (new swap chain or mode change); consumers then reopen it.
    unsigned int Generation;
    unsigned int Width;
    unsigned int Height;
    unsigned int Format;          // DXGI_FORMAT of the texture
    unsigned int AdapterLuidLow;  // render adapter the texture lives on
    int          AdapterLuidHigh;
    unsigned int FramesPublished; // frames copied into the texture
    unsigned int FramesSkipped;   // frames dropped because the consumer held it
    unsigned int Failed;          // nonzero: the driver could not create it
    unsigned int HardwareCursor;  // nonzero: cursor is published, not drawn into frames
} DRAWSPLAY_GET_FRAME_SURFACE_OUT;

#pragma pack(pop)
