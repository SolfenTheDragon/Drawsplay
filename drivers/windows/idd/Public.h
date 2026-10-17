// Public.h - control protocol shared between the DrawSplay IddCx virtual
// display driver and the host.
//
// The host opens the driver's device interface and issues these buffered
// IOCTLs to plug and unplug virtual monitors. Each monitor starts at the
// requested mode (marked preferred) and also offers the tablet's native size
// plus standard 720p-4K resolutions in Windows Display Settings. All monitors created
// through a control handle are torn down automatically when that handle is
// closed, so a host crash never leaves an orphaned virtual display.
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

#pragma pack(pop)
