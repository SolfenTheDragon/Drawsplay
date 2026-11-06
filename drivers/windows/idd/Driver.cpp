/*++

DrawSplay IddCx indirect display driver.

Derived from the Microsoft IddSampleDriver (Microsoft Public License, see the
LICENSE file in this directory). The sample's static, hard-coded monitor set
is replaced by a dynamic model: the DrawSplay host opens the driver's device
interface (Public.h) and plugs exactly one virtual monitor per streaming
session at the connected tablet's native resolution and refresh rate. Every
monitor created through a control handle is removed automatically when the
handle closes, so a host crash never leaves an orphaned virtual display.

Environment: User Mode, UMDF

--*/

#include "Driver.h"

// initguid.h must follow the system headers (pulled in by Driver.h) and
// precede the first include of Public.h so the device-interface GUID is
// instantiated here exactly once.
#include <initguid.h>
#include "Public.h"

using namespace std;
using namespace Microsoft::IndirectDisp;
using namespace Microsoft::WRL;

#pragma region helpers

static constexpr DWORD DRAWSPLAY_MAX_MONITORS = 8;

// Hand-off for an EvtIddCxAdapterInitFinished that fires before
// InitAdapter has attached the device context to the new adapter object.
static constexpr LONG kNoParkedStatus = (LONG) 0x7FFFFFFF;
static volatile LONG g_ParkedAdapterInitStatus = kNoParkedStatus;

// Swap-chain device creation can fail transiently while a GPU driver is
// being updated or reset (common with NVIDIA/AMD driver installs and hybrid
// graphics power transitions); retry briefly before giving the swap chain
// back to the OS.
static constexpr int kDeviceInitAttempts = 3;
static constexpr DWORD kDeviceInitRetryMs = 100;

static inline void FillSignalInfo(DISPLAYCONFIG_VIDEO_SIGNAL_INFO& Mode, DWORD Width, DWORD Height, DWORD VSync, bool bMonitorMode)
{
    Mode.totalSize.cx = Mode.activeSize.cx = Width;
    Mode.totalSize.cy = Mode.activeSize.cy = Height;

    Mode.AdditionalSignalInfo.vSyncFreqDivider = bMonitorMode ? 0 : 1;
    Mode.AdditionalSignalInfo.videoStandard = 255;

    Mode.vSyncFreq.Numerator = VSync;
    Mode.vSyncFreq.Denominator = 1;
    Mode.hSyncFreq.Numerator = VSync * Height;
    Mode.hSyncFreq.Denominator = 1;

    Mode.scanLineOrdering = DISPLAYCONFIG_SCANLINE_ORDERING_PROGRESSIVE;

    Mode.pixelRate = ((UINT64) VSync) * ((UINT64) Width) * ((UINT64) Height);
}

static IDDCX_MONITOR_MODE CreateIddCxMonitorMode(DWORD Width, DWORD Height, DWORD VSync, IDDCX_MONITOR_MODE_ORIGIN Origin = IDDCX_MONITOR_MODE_ORIGIN_DRIVER)
{
    IDDCX_MONITOR_MODE Mode = {};

    Mode.Size = sizeof(Mode);
    Mode.Origin = Origin;
    FillSignalInfo(Mode.MonitorVideoSignalInfo, Width, Height, VSync, true);

    return Mode;
}

static IDDCX_TARGET_MODE CreateIddCxTargetMode(DWORD Width, DWORD Height, DWORD VSync)
{
    IDDCX_TARGET_MODE Mode = {};

    Mode.Size = sizeof(Mode);
    FillSignalInfo(Mode.TargetVideoSignalInfo.targetVideoSignalInfo, Width, Height, VSync, false);

    return Mode;
}

// Sanitize a host-requested mode into a plausible monitor mode.
static void NormalizeMode(DWORD& Width, DWORD& Height, DWORD& RefreshHz)
{
    if (Width < 640) Width = 640;
    if (Height < 480) Height = 480;
    if (Width > 7680) Width = 7680;
    if (Height > 4320) Height = 4320;
    if (RefreshHz < 24) RefreshHz = 60;
    if (RefreshHz > 240) RefreshHz = 240;
}

struct ModeSize
{
    DWORD Width;
    DWORD Height;
};

// Standard 16:9 and 16:10 resolutions (720p up to 4K) offered in Windows
// Display Settings in addition to the initial and native modes.
static const ModeSize kStandardModes[] = {
    { 1280, 720 }, { 1280, 800 }, { 1366, 768 }, { 1440, 900 },
    { 1600, 900 }, { 1680, 1050 }, { 1920, 1080 }, { 1920, 1200 },
    { 2560, 1440 }, { 2560, 1600 }, { 2880, 1800 }, { 3200, 1800 },
    { 3840, 2160 }, { 3840, 2400 },
};

// Distinct resolutions for a monitor: the initial (preferred) mode first,
// then the native panel size, then the standard list.
static vector<ModeSize> MonitorResolutions(DWORD Width, DWORD Height, DWORD NativeWidth, DWORD NativeHeight)
{
    vector<ModeSize> Sizes;
    auto add = [&Sizes](DWORD w, DWORD h)
    {
        if (w == 0 || h == 0)
        {
            return;
        }
        for (const auto& s : Sizes)
        {
            if (s.Width == w && s.Height == h)
            {
                return;
            }
        }
        Sizes.push_back({ w, h });
    };
    add(Width, Height);
    add(NativeWidth, NativeHeight);
    for (const auto& s : kStandardModes)
    {
        add(s.Width, s.Height);
    }
    return Sizes;
}

// Refresh rates offered for every resolution: the tablet's rate, then 60 Hz.
static vector<DWORD> MonitorRefreshRates(DWORD RefreshHz)
{
    vector<DWORD> Rates{ RefreshHz };
    if (RefreshHz != 60)
    {
        Rates.push_back(60);
    }
    return Rates;
}

#pragma endregion

extern "C" DRIVER_INITIALIZE DriverEntry;

EVT_WDF_DRIVER_DEVICE_ADD DrawSplayDeviceAdd;
EVT_WDF_DEVICE_D0_ENTRY DrawSplayDeviceD0Entry;
EVT_WDF_FILE_CLEANUP DrawSplayFileCleanup;

EVT_IDD_CX_ADAPTER_INIT_FINISHED DrawSplayAdapterInitFinished;
EVT_IDD_CX_ADAPTER_COMMIT_MODES DrawSplayAdapterCommitModes;
EVT_IDD_CX_DEVICE_IO_CONTROL DrawSplayDeviceIoControl;

EVT_IDD_CX_PARSE_MONITOR_DESCRIPTION DrawSplayParseMonitorDescription;
EVT_IDD_CX_MONITOR_GET_DEFAULT_DESCRIPTION_MODES DrawSplayMonitorGetDefaultModes;
EVT_IDD_CX_MONITOR_QUERY_TARGET_MODES DrawSplayMonitorQueryModes;

EVT_IDD_CX_MONITOR_ASSIGN_SWAPCHAIN DrawSplayMonitorAssignSwapChain;
EVT_IDD_CX_MONITOR_UNASSIGN_SWAPCHAIN DrawSplayMonitorUnassignSwapChain;

struct IndirectDeviceContextWrapper
{
    IndirectDeviceContext* pContext;

    void Cleanup()
    {
        delete pContext;
        pContext = nullptr;
    }
};

struct IndirectMonitorContextWrapper
{
    IndirectMonitorContext* pContext;

    void Cleanup()
    {
        delete pContext;
        pContext = nullptr;
    }
};

WDF_DECLARE_CONTEXT_TYPE(IndirectDeviceContextWrapper);
WDF_DECLARE_CONTEXT_TYPE(IndirectMonitorContextWrapper);

extern "C" BOOL WINAPI DllMain(
    _In_ HINSTANCE hInstance,
    _In_ UINT dwReason,
    _In_opt_ LPVOID lpReserved)
{
    UNREFERENCED_PARAMETER(hInstance);
    UNREFERENCED_PARAMETER(lpReserved);
    UNREFERENCED_PARAMETER(dwReason);

    return TRUE;
}

_Use_decl_annotations_
extern "C" NTSTATUS DriverEntry(
    PDRIVER_OBJECT  pDriverObject,
    PUNICODE_STRING pRegistryPath
)
{
    WDF_DRIVER_CONFIG Config;
    NTSTATUS Status;

    WDF_OBJECT_ATTRIBUTES Attributes;
    WDF_OBJECT_ATTRIBUTES_INIT(&Attributes);

    WDF_DRIVER_CONFIG_INIT(&Config, DrawSplayDeviceAdd);

    Status = WdfDriverCreate(pDriverObject, pRegistryPath, &Attributes, &Config, WDF_NO_HANDLE);
    if (!NT_SUCCESS(Status))
    {
        return Status;
    }

    return Status;
}

_Use_decl_annotations_
NTSTATUS DrawSplayDeviceAdd(WDFDRIVER Driver, PWDFDEVICE_INIT pDeviceInit)
{
    NTSTATUS Status = STATUS_SUCCESS;
    WDF_PNPPOWER_EVENT_CALLBACKS PnpPowerCallbacks;

    UNREFERENCED_PARAMETER(Driver);

    WDF_PNPPOWER_EVENT_CALLBACKS_INIT(&PnpPowerCallbacks);
    PnpPowerCallbacks.EvtDeviceD0Entry = DrawSplayDeviceD0Entry;
    WdfDeviceInitSetPnpPowerEventCallbacks(pDeviceInit, &PnpPowerCallbacks);

    // Track control-handle lifetime so monitors are torn down on close/crash.
    WDF_FILEOBJECT_CONFIG FileConfig;
    WDF_FILEOBJECT_CONFIG_INIT(&FileConfig, nullptr, nullptr, DrawSplayFileCleanup);
    WdfDeviceInitSetFileObjectConfig(pDeviceInit, &FileConfig, WDF_NO_OBJECT_ATTRIBUTES);

    IDD_CX_CLIENT_CONFIG IddConfig;
    IDD_CX_CLIENT_CONFIG_INIT(&IddConfig);

    // Custom IoDeviceControl requests must go through this callback; IddCx
    // otherwise redirects them to an internal queue.
    IddConfig.EvtIddCxDeviceIoControl = DrawSplayDeviceIoControl;

    IddConfig.EvtIddCxAdapterInitFinished = DrawSplayAdapterInitFinished;

    IddConfig.EvtIddCxParseMonitorDescription = DrawSplayParseMonitorDescription;
    IddConfig.EvtIddCxMonitorGetDefaultDescriptionModes = DrawSplayMonitorGetDefaultModes;
    IddConfig.EvtIddCxMonitorQueryTargetModes = DrawSplayMonitorQueryModes;
    IddConfig.EvtIddCxAdapterCommitModes = DrawSplayAdapterCommitModes;
    IddConfig.EvtIddCxMonitorAssignSwapChain = DrawSplayMonitorAssignSwapChain;
    IddConfig.EvtIddCxMonitorUnassignSwapChain = DrawSplayMonitorUnassignSwapChain;

    Status = IddCxDeviceInitConfig(pDeviceInit, &IddConfig);
    if (!NT_SUCCESS(Status))
    {
        return Status;
    }

    WDF_OBJECT_ATTRIBUTES Attr;
    WDF_OBJECT_ATTRIBUTES_INIT_CONTEXT_TYPE(&Attr, IndirectDeviceContextWrapper);
    Attr.EvtCleanupCallback = [](WDFOBJECT Object)
    {
        auto* pContext = WdfObjectGet_IndirectDeviceContextWrapper(Object);
        if (pContext)
        {
            pContext->Cleanup();
        }
    };

    WDFDEVICE Device = nullptr;
    Status = WdfDeviceCreate(&pDeviceInit, &Attr, &Device);
    if (!NT_SUCCESS(Status))
    {
        return Status;
    }

    Status = IddCxDeviceInitialize(Device);
    if (!NT_SUCCESS(Status))
    {
        return Status;
    }

    // Expose a device interface the host can open to issue control IOCTLs.
    Status = WdfDeviceCreateDeviceInterface(Device, &GUID_DEVINTERFACE_DRAWSPLAY_IDD, nullptr);
    if (!NT_SUCCESS(Status))
    {
        return Status;
    }

    auto* pContext = WdfObjectGet_IndirectDeviceContextWrapper(Device);
    pContext->pContext = new IndirectDeviceContext(Device);

    return Status;
}

_Use_decl_annotations_
NTSTATUS DrawSplayDeviceD0Entry(WDFDEVICE Device, WDF_POWER_DEVICE_STATE PreviousState)
{
    UNREFERENCED_PARAMETER(PreviousState);

    auto* pContext = WdfObjectGet_IndirectDeviceContextWrapper(Device);
    pContext->pContext->InitAdapter();

    return STATUS_SUCCESS;
}

_Use_decl_annotations_
VOID DrawSplayFileCleanup(WDFFILEOBJECT FileObject)
{
    // When the host's control handle closes (graceful or crash), remove the
    // monitors it created so the virtual display never outlives the session.
    // Other handles (the capture helper's) own no monitors and remove none.
    WDFDEVICE Device = WdfFileObjectGetDevice(FileObject);
    auto* pContext = WdfObjectGet_IndirectDeviceContextWrapper(Device);
    if (pContext && pContext->pContext)
    {
        pContext->pContext->RemoveMonitorsOwnedBy(FileObject);
    }
}

#pragma region Direct3DDevice

Direct3DDevice::Direct3DDevice(LUID AdapterLuid) : AdapterLuid(AdapterLuid)
{
}

Direct3DDevice::Direct3DDevice()
{
    AdapterLuid = LUID{};
}

HRESULT Direct3DDevice::Init()
{
    HRESULT hr = CreateDXGIFactory2(0, IID_PPV_ARGS(&DxgiFactory));
    if (FAILED(hr))
    {
        return hr;
    }

    hr = DxgiFactory->EnumAdapterByLuid(AdapterLuid, IID_PPV_ARGS(&Adapter));
    if (FAILED(hr))
    {
        return hr;
    }

    // The render adapter IddCx hands us can be anything from a current
    // NVIDIA/AMD/Intel GPU to an older Intel iGPU limited to feature level
    // 10.x, or the Microsoft Basic Render Driver while a vendor driver is
    // being updated. The swap chain only needs a device on that adapter, so
    // accept every feature level down to 9.1.
    static const D3D_FEATURE_LEVEL kFeatureLevels[] = {
        D3D_FEATURE_LEVEL_11_1, D3D_FEATURE_LEVEL_11_0,
        D3D_FEATURE_LEVEL_10_1, D3D_FEATURE_LEVEL_10_0,
        D3D_FEATURE_LEVEL_9_3, D3D_FEATURE_LEVEL_9_2, D3D_FEATURE_LEVEL_9_1,
    };
    // Some older drivers reject the BGRA flag; the swap chain doesn't need it.
    const UINT kFlags[] = { D3D11_CREATE_DEVICE_BGRA_SUPPORT, 0 };
    for (UINT Flags : kFlags)
    {
        hr = D3D11CreateDevice(Adapter.Get(), D3D_DRIVER_TYPE_UNKNOWN, nullptr, Flags,
            kFeatureLevels, ARRAYSIZE(kFeatureLevels), D3D11_SDK_VERSION,
            &Device, &FeatureLevel, &DeviceContext);
        if (hr == E_INVALIDARG)
        {
            // A D3D 11.0 runtime rejects the whole list because of 11_1.
            hr = D3D11CreateDevice(Adapter.Get(), D3D_DRIVER_TYPE_UNKNOWN, nullptr, Flags,
                kFeatureLevels + 1, ARRAYSIZE(kFeatureLevels) - 1, D3D11_SDK_VERSION,
                &Device, &FeatureLevel, &DeviceContext);
        }
        if (SUCCEEDED(hr))
        {
            return S_OK;
        }
    }
    return hr;
}

#pragma endregion

#pragma region SwapChainProcessor

FrameExport::~FrameExport()
{
    if (Cursor)
    {
        UnmapViewOfFile(Cursor);
    }
    if (CursorMapping)
    {
        CloseHandle(CursorMapping);
    }
}

SwapChainProcessor::SwapChainProcessor(IDDCX_SWAPCHAIN hSwapChain, shared_ptr<Direct3DDevice> Device, HANDLE NewFrameEvent,
    IDDCX_MONITOR Monitor, DWORD MonitorId, shared_ptr<FrameExport> Export)
    : m_hSwapChain(hSwapChain), m_Device(Device), m_hAvailableBufferEvent(NewFrameEvent),
      m_Monitor(Monitor), m_MonitorId(MonitorId), m_Export(move(Export))
{
    m_hTerminateEvent.Attach(CreateEvent(nullptr, FALSE, FALSE, nullptr));
    m_hThread.Attach(CreateThread(nullptr, 0, RunThread, this, 0, nullptr));
}

SwapChainProcessor::~SwapChainProcessor()
{
    SetEvent(m_hTerminateEvent.Get());

    if (m_hThread.Get())
    {
        WaitForSingleObject(m_hThread.Get(), INFINITE);
    }
}

DWORD CALLBACK SwapChainProcessor::RunThread(LPVOID Argument)
{
    reinterpret_cast<SwapChainProcessor*>(Argument)->Run();
    return 0;
}

void SwapChainProcessor::Run()
{
    DWORD AvTask = 0;
    HANDLE AvTaskHandle = AvSetMmThreadCharacteristicsW(L"Distribution", &AvTask);

    RunCore();
    ReleaseSharedTexture();
    // The next swap chain starts with the OS drawing the cursor again until
    // it is handed over anew; hide the published one meanwhile.
    HideSharedCursor();

    WdfObjectDelete((WDFOBJECT)m_hSwapChain);
    m_hSwapChain = nullptr;

    if (AvTaskHandle)
    {
        AvRevertMmThreadCharacteristics(AvTaskHandle);
    }
}

void SwapChainProcessor::RunCore()
{
    ComPtr<IDXGIDevice> DxgiDevice;
    HRESULT hr = m_Device->Device.As(&DxgiDevice);
    if (FAILED(hr))
    {
        return;
    }

    IDARG_IN_SWAPCHAINSETDEVICE SetDevice = {};
    SetDevice.pDevice = DxgiDevice.Get();

    hr = IddCxSwapChainSetDevice(m_hSwapChain, &SetDevice);
    if (FAILED(hr))
    {
        return;
    }

    for (;;)
    {
        if (!m_CursorSetupTried && m_Export &&
            InterlockedCompareExchange(&m_Export->CursorRequested, 0, 0) != 0)
        {
            SetupHardwareCursor();
        }
        // Cursor changes are published even while frames stream back to back.
        if (m_hCursorEvent.IsValid() && WaitForSingleObject(m_hCursorEvent.Get(), 0) == WAIT_OBJECT_0)
        {
            PublishCursor();
        }
        FlushPendingFrame();

        ComPtr<IDXGIResource> AcquiredBuffer;

        IDARG_OUT_RELEASEANDACQUIREBUFFER Buffer = {};
        hr = IddCxSwapChainReleaseAndAcquireBuffer(m_hSwapChain, &Buffer);

        if (hr == E_PENDING)
        {
            HANDLE WaitHandles[] =
            {
                m_hAvailableBufferEvent,
                m_hTerminateEvent.Get(),
                m_hCursorEvent.Get()
            };
            const DWORD Count = m_hCursorEvent.IsValid() ? 3 : 2;
            // A parked frame is retried every 2 ms until the consumer frees
            // the shared texture (it holds it only for a GPU copy).
            DWORD WaitResult = WaitForMultipleObjects(Count, WaitHandles, FALSE, m_FramePending ? 2 : 16);
            if (WaitResult == WAIT_OBJECT_0 || WaitResult == WAIT_TIMEOUT)
            {
                continue;
            }
            else if (WaitResult == WAIT_OBJECT_0 + 2)
            {
                PublishCursor();
                continue;
            }
            else if (WaitResult == WAIT_OBJECT_0 + 1)
            {
                break;
            }
            else
            {
                hr = HRESULT_FROM_WIN32(WaitResult);
                break;
            }
        }
        else if (SUCCEEDED(hr))
        {
            // With frame export enabled (the native capture helper asked for
            // it) the frame is copied into the shared texture; otherwise the
            // host captures out-of-band via DDA (ffmpeg ddagrab) and the
            // surface is simply released.
            AcquiredBuffer.Attach(Buffer.MetaData.pSurface);
            if (m_Export && InterlockedCompareExchange(&m_Export->Requested, 0, 0) != 0)
            {
                ExportFrame(AcquiredBuffer.Get());
            }
            AcquiredBuffer.Reset();

            hr = IddCxSwapChainFinishedProcessingFrame(m_hSwapChain);
            if (FAILED(hr))
            {
                break;
            }
        }
        else
        {
            break;
        }
    }
}

// Security descriptor for a monitor's shared texture and cursor block:
// SYSTEM and the user who created the monitor only, so another user signed
// in to the same PC (fast user switching, RDP) cannot open them. Returns
// false (and export stays off) when the owner is unknown.
static bool OwnerOnlySecurity(const FrameExport& Export, PSECURITY_DESCRIPTOR* ppSd)
{
    if (Export.OwnerSid.empty() || !IsValidSid((PSID) Export.OwnerSid.data()))
    {
        return false;
    }
    LPWSTR SidString = nullptr;
    if (!ConvertSidToStringSidW((PSID) Export.OwnerSid.data(), &SidString))
    {
        return false;
    }
    wchar_t Sddl[256];
    swprintf_s(Sddl, L"D:P(A;;GA;;;SY)(A;;GA;;;%s)", SidString);
    LocalFree(SidString);
    return ConvertStringSecurityDescriptorToSecurityDescriptorW(Sddl, SDDL_REVISION_1, ppSd, nullptr) != FALSE;
}

bool SwapChainProcessor::EnsureSharedTexture(const D3D11_TEXTURE2D_DESC& SourceDesc)
{
    if (m_SharedTexture &&
        m_SharedDesc.Width == SourceDesc.Width &&
        m_SharedDesc.Height == SourceDesc.Height &&
        m_SharedDesc.Format == SourceDesc.Format)
    {
        return true;
    }
    ReleaseSharedTexture();

    D3D11_TEXTURE2D_DESC Desc = {};
    Desc.Width = SourceDesc.Width;
    Desc.Height = SourceDesc.Height;
    Desc.MipLevels = 1;
    Desc.ArraySize = 1;
    Desc.Format = SourceDesc.Format;
    Desc.SampleDesc.Count = 1;
    Desc.Usage = D3D11_USAGE_DEFAULT;
    // Consumers read it as a video-processor input or shader resource.
    Desc.BindFlags = D3D11_BIND_SHADER_RESOURCE | D3D11_BIND_RENDER_TARGET;
    Desc.MiscFlags = D3D11_RESOURCE_MISC_SHARED_NTHANDLE | D3D11_RESOURCE_MISC_SHARED_KEYEDMUTEX;

    ComPtr<ID3D11Texture2D> Texture;
    HRESULT hr = m_Device->Device->CreateTexture2D(&Desc, nullptr, &Texture);
    ComPtr<IDXGIKeyedMutex> Mutex;
    ComPtr<IDXGIResource1> Resource;
    if (SUCCEEDED(hr)) hr = Texture.As(&Mutex);
    if (SUCCEEDED(hr)) hr = Texture.As(&Resource);
    if (FAILED(hr))
    {
        InterlockedExchange(&m_Export->Failed, 1);
        return false;
    }

    PSECURITY_DESCRIPTOR pSd = nullptr;
    if (!OwnerOnlySecurity(*m_Export, &pSd))
    {
        InterlockedExchange(&m_Export->Failed, 1);
        return false;
    }
    SECURITY_ATTRIBUTES Sa = { sizeof(Sa), pSd, FALSE };

    // Each texture gets a fresh generation, so a consumer holding the old
    // one can tell it was replaced.
    LONG Generation = InterlockedCompareExchange(&m_Export->Generation, 0, 0) + 1;
    wchar_t Name[96];
    swprintf_s(Name, DRAWSPLAY_FRAME_SURFACE_NAME_FORMAT, m_MonitorId, (unsigned) Generation);

    HANDLE Shared = nullptr;
    hr = Resource->CreateSharedHandle(&Sa, DXGI_SHARED_RESOURCE_READ | DXGI_SHARED_RESOURCE_WRITE, Name, &Shared);
    LocalFree(pSd);
    if (FAILED(hr))
    {
        InterlockedExchange(&m_Export->Failed, 1);
        return false;
    }

    m_SharedTexture = Texture;
    m_SharedMutex = Mutex;
    m_SharedHandle.Attach(Shared);
    m_SharedDesc = Desc;

    m_Export->Width = Desc.Width;
    m_Export->Height = Desc.Height;
    m_Export->Format = (UINT) Desc.Format;
    m_Export->AdapterLuid = m_Device->AdapterLuid;
    InterlockedExchange(&m_Export->Failed, 0);
    InterlockedExchange(&m_Export->Generation, Generation);
    return true;
}

void SwapChainProcessor::ReleaseSharedTexture()
{
    m_SharedMutex.Reset();
    m_SharedTexture.Reset();
    m_SharedHandle.Close();
    m_SharedDesc = {};
    m_PendingTexture.Reset();
    m_FramePending = false;
}

void SwapChainProcessor::SetupHardwareCursor()
{
    m_CursorSetupTried = true; // once per swap chain, success or not

    if (!m_Export->Cursor)
    {
        PSECURITY_DESCRIPTOR pSd = nullptr;
        if (!OwnerOnlySecurity(*m_Export, &pSd))
        {
            return;
        }
        SECURITY_ATTRIBUTES Sa = { sizeof(Sa), pSd, FALSE };
        wchar_t Name[96];
        swprintf_s(Name, DRAWSPLAY_CURSOR_NAME_FORMAT, m_MonitorId);
        HANDLE Mapping = CreateFileMappingW(INVALID_HANDLE_VALUE, &Sa, PAGE_READWRITE, 0,
            (DWORD) sizeof(DRAWSPLAY_CURSOR_SHARED), Name);
        LocalFree(pSd);
        if (!Mapping)
        {
            return;
        }
        auto* View = static_cast<DRAWSPLAY_CURSOR_SHARED*>(
            MapViewOfFile(Mapping, FILE_MAP_WRITE, 0, 0, sizeof(DRAWSPLAY_CURSOR_SHARED)));
        if (!View)
        {
            CloseHandle(Mapping);
            return;
        }
        m_Export->CursorMapping = Mapping;
        m_Export->Cursor = View;
    }

    m_hCursorEvent.Attach(CreateEvent(nullptr, FALSE, FALSE, nullptr));
    if (!m_hCursorEvent.IsValid())
    {
        return;
    }

    // Alpha cursors only: XOR/inverting cursors (and any larger than the
    // maximum) keep being drawn into the frames by the OS, so nothing is
    // ever lost when the consumer cannot reproduce them.
    IDARG_IN_SETUP_HWCURSOR Setup = {};
    Setup.CursorInfo.Size = sizeof(Setup.CursorInfo);
    Setup.CursorInfo.ColorXorCursorSupport = IDDCX_XOR_CURSOR_SUPPORT_NONE;
    Setup.CursorInfo.AlphaCursorSupport = TRUE;
    Setup.CursorInfo.MaxX = DRAWSPLAY_CURSOR_MAX;
    Setup.CursorInfo.MaxY = DRAWSPLAY_CURSOR_MAX;
    Setup.hNewCursorDataAvailable = m_hCursorEvent.Get();
    if (!NT_SUCCESS(IddCxMonitorSetupHardwareCursor(m_Monitor, &Setup)))
    {
        m_hCursorEvent.Close();
        return;
    }
    m_CursorBuffer.resize(DRAWSPLAY_CURSOR_MAX * DRAWSPLAY_CURSOR_MAX * 4);
    m_LastShapeId = (DWORD) -1;
    InterlockedExchange(&m_Export->HardwareCursor, 1);
    PublishCursor();
}

void SwapChainProcessor::PublishCursor()
{
    DRAWSPLAY_CURSOR_SHARED* c = m_Export ? m_Export->Cursor : nullptr;
    if (!c || m_CursorBuffer.empty())
    {
        return;
    }
    IDARG_IN_QUERY_HWCURSOR In = {};
    In.LastShapeId = m_LastShapeId;
    In.ShapeBufferSizeInBytes = (UINT) m_CursorBuffer.size();
    In.pShapeBuffer = m_CursorBuffer.data();
    IDARG_OUT_QUERY_HWCURSOR Out = {};
    if (!NT_SUCCESS(IddCxMonitorQueryHardwareCursor(m_Monitor, &In, &Out)))
    {
        return;
    }

    InterlockedIncrement(&c->Seq); // odd: writing
    MemoryBarrier();
    c->Visible = Out.IsCursorVisible ? 1 : 0;
    c->X = Out.X;
    c->Y = Out.Y;
    if (Out.IsCursorVisible && Out.IsCursorShapeUpdated)
    {
        const IDDCX_CURSOR_SHAPE_INFO& Info = Out.CursorShapeInfo;
        const UINT W = min(Info.Width, (UINT) DRAWSPLAY_CURSOR_MAX);
        const UINT H = min(Info.Height, (UINT) DRAWSPLAY_CURSOR_MAX);
        const bool Masked = Info.CursorType == IDDCX_CURSOR_SHAPE_TYPE_MASKED_COLOR;
        for (UINT y = 0; y < H; ++y)
        {
            const BYTE* Src = m_CursorBuffer.data() + (size_t) y * Info.Pitch;
            BYTE* Dst = c->Shape + (size_t) y * W * 4;
            memcpy(Dst, Src, (size_t) W * 4);
            if (Masked)
            {
                // Not expected (XOR support is off), but if one arrives,
                // approximate it: mask 0 = opaque colour; mask 0xFF = XOR,
                // shown opaque where it changes the screen.
                for (UINT x = 0; x < W; ++x)
                {
                    BYTE* p = Dst + x * 4;
                    const bool Xor = p[3] != 0;
                    const bool Black = p[0] == 0 && p[1] == 0 && p[2] == 0;
                    p[3] = (Xor && Black) ? 0 : 0xFF;
                }
            }
        }
        c->Width = W;
        c->Height = H;
        c->XHot = Info.XHot;
        c->YHot = Info.YHot;
        c->ShapeId = Info.ShapeId;
        m_LastShapeId = Info.ShapeId;
    }
    MemoryBarrier();
    InterlockedIncrement(&c->Seq); // even: stable
}

void SwapChainProcessor::HideSharedCursor()
{
    if (m_Export)
    {
        InterlockedExchange(&m_Export->HardwareCursor, 0);
    }
    DRAWSPLAY_CURSOR_SHARED* c = m_Export ? m_Export->Cursor : nullptr;
    if (!c || !m_hCursorEvent.IsValid())
    {
        return;
    }
    InterlockedIncrement(&c->Seq);
    MemoryBarrier();
    c->Visible = 0;
    MemoryBarrier();
    InterlockedIncrement(&c->Seq);
}

void SwapChainProcessor::ExportFrame(IDXGIResource* pSurface)
{
    ComPtr<ID3D11Texture2D> Source;
    if (FAILED(pSurface->QueryInterface(IID_PPV_ARGS(&Source))))
    {
        return;
    }
    D3D11_TEXTURE2D_DESC SourceDesc = {};
    Source->GetDesc(&SourceDesc);
    if (!EnsureSharedTexture(SourceDesc))
    {
        return;
    }

    // Never wait here: this thread paces the desktop's presentation. If the
    // consumer has not taken the previous frame yet, this one is parked in
    // a private texture (replacing any older parked frame) and published by
    // FlushPendingFrame as soon as the shared texture is free again.
    HRESULT hr = m_SharedMutex->AcquireSync(0, 0);
    if (hr == static_cast<HRESULT>(WAIT_TIMEOUT))
    {
        if (!m_PendingTexture)
        {
            D3D11_TEXTURE2D_DESC Desc = m_SharedDesc;
            Desc.MiscFlags = 0;
            if (FAILED(m_Device->Device->CreateTexture2D(&Desc, nullptr, &m_PendingTexture)))
            {
                InterlockedIncrement(&m_Export->FramesSkipped);
                return;
            }
        }
        m_Device->DeviceContext->CopyResource(m_PendingTexture.Get(), Source.Get());
        if (m_FramePending)
        {
            InterlockedIncrement(&m_Export->FramesSkipped); // the older parked frame is replaced
        }
        m_FramePending = true;
        return;
    }
    if (FAILED(hr))
    {
        // Device removed or the mutex was abandoned: rebuild on next frame.
        ReleaseSharedTexture();
        return;
    }
    m_Device->DeviceContext->CopyResource(m_SharedTexture.Get(), Source.Get());
    m_SharedMutex->ReleaseSync(1);
    m_FramePending = false; // superseded by this newer frame
    InterlockedIncrement(&m_Export->FramesPublished);
}

void SwapChainProcessor::FlushPendingFrame()
{
    if (!m_FramePending || !m_SharedMutex || !m_PendingTexture)
    {
        return;
    }
    HRESULT hr = m_SharedMutex->AcquireSync(0, 0);
    if (hr == static_cast<HRESULT>(WAIT_TIMEOUT))
    {
        return; // still held; try again shortly
    }
    if (FAILED(hr))
    {
        ReleaseSharedTexture();
        return;
    }
    m_Device->DeviceContext->CopyResource(m_SharedTexture.Get(), m_PendingTexture.Get());
    m_SharedMutex->ReleaseSync(1);
    m_FramePending = false;
    InterlockedIncrement(&m_Export->FramesPublished);
}

#pragma endregion

#pragma region IndirectDeviceContext

IndirectDeviceContext::IndirectDeviceContext(_In_ WDFDEVICE WdfDevice) :
    m_Adapter(nullptr),
    m_WdfDevice(WdfDevice)
{
}

IndirectDeviceContext::~IndirectDeviceContext()
{
}

void IndirectDeviceContext::InitAdapter()
{
    IDDCX_ADAPTER_CAPS AdapterCaps = {};
    AdapterCaps.Size = sizeof(AdapterCaps);

    AdapterCaps.MaxMonitorsSupported = DRAWSPLAY_MAX_MONITORS;
    AdapterCaps.EndPointDiagnostics.Size = sizeof(AdapterCaps.EndPointDiagnostics);
    AdapterCaps.EndPointDiagnostics.GammaSupport = IDDCX_FEATURE_IMPLEMENTATION_NONE;
    AdapterCaps.EndPointDiagnostics.TransmissionType = IDDCX_TRANSMISSION_TYPE_WIRED_OTHER;

    AdapterCaps.EndPointDiagnostics.pEndPointFriendlyName = L"DrawSplay Virtual Display";
    AdapterCaps.EndPointDiagnostics.pEndPointManufacturerName = L"DrawSplay";
    AdapterCaps.EndPointDiagnostics.pEndPointModelName = L"DrawSplay Tablet Display";

    IDDCX_ENDPOINT_VERSION Version = {};
    Version.Size = sizeof(Version);
    Version.MajorVer = 1;
    AdapterCaps.EndPointDiagnostics.pFirmwareVersion = &Version;
    AdapterCaps.EndPointDiagnostics.pHardwareVersion = &Version;

    WDF_OBJECT_ATTRIBUTES Attr;
    WDF_OBJECT_ATTRIBUTES_INIT_CONTEXT_TYPE(&Attr, IndirectDeviceContextWrapper);

    IDARG_IN_ADAPTER_INIT AdapterInit = {};
    AdapterInit.WdfDevice = m_WdfDevice;
    AdapterInit.pCaps = &AdapterCaps;
    AdapterInit.ObjectAttributes = &Attr;

    IDARG_OUT_ADAPTER_INIT AdapterInitOut;
    NTSTATUS Status = IddCxAdapterInitAsync(&AdapterInit, &AdapterInitOut);

    if (NT_SUCCESS(Status))
    {
        m_Adapter = AdapterInitOut.AdapterObject;

        auto* pContext = WdfObjectGet_IndirectDeviceContextWrapper(AdapterInitOut.AdapterObject);
        InterlockedExchangePointer(reinterpret_cast<PVOID volatile*>(&pContext->pContext), this);

        // EvtIddCxAdapterInitFinished may have run before the context was
        // attached; apply any status it parked.
        LONG Parked = InterlockedExchange(&g_ParkedAdapterInitStatus, kNoParkedStatus);
        if (Parked != kNoParkedStatus)
        {
            SetAdapterInitStatus(Parked);
        }
    }
}

void IndirectDeviceContext::SetAdapterInitStatus(NTSTATUS Status)
{
    InterlockedExchange(&m_AdapterReady, NT_SUCCESS(Status) ? 1 : 0);
}

NTSTATUS IndirectDeviceContext::AddMonitor(DWORD Width, DWORD Height, DWORD RefreshHz, DWORD NativeWidth, DWORD NativeHeight, WDFFILEOBJECT Owner,
    const vector<BYTE>& OwnerSid, DWORD* pMonitorId)
{
    if (!m_Adapter || InterlockedCompareExchange(&m_AdapterReady, 0, 0) == 0)
    {
        return STATUS_DEVICE_NOT_READY;
    }

    AcquireSRWLockExclusive(&m_Lock);
    NTSTATUS Status = AddMonitorLocked(Width, Height, RefreshHz, NativeWidth, NativeHeight, Owner, OwnerSid, pMonitorId);
    ReleaseSRWLockExclusive(&m_Lock);
    return Status;
}

NTSTATUS IndirectDeviceContext::AddMonitorLocked(DWORD Width, DWORD Height, DWORD RefreshHz, DWORD NativeWidth, DWORD NativeHeight, WDFFILEOBJECT Owner,
    const vector<BYTE>& OwnerSid, DWORD* pMonitorId)
{
    if (m_Monitors.size() >= DRAWSPLAY_MAX_MONITORS)
    {
        return STATUS_QUOTA_EXCEEDED;
    }

    NormalizeMode(Width, Height, RefreshHz);
    if (NativeWidth == 0 || NativeHeight == 0)
    {
        NativeWidth = Width;
        NativeHeight = Height;
    }
    else
    {
        DWORD NativeHz = RefreshHz;
        NormalizeMode(NativeWidth, NativeHeight, NativeHz);
    }

    // The monitor id stays unique for the life of the driver (it names the
    // shared surfaces), but the IddCx connector index must stay below
    // MaxMonitorsSupported, so it comes from a pool of reusable slots.
    int Slot = m_Connectors->Acquire(DRAWSPLAY_MAX_MONITORS);
    if (Slot < 0)
    {
        return STATUS_QUOTA_EXCEEDED;
    }
    DWORD MonitorId = m_NextMonitorId++;

    WDF_OBJECT_ATTRIBUTES Attr;
    WDF_OBJECT_ATTRIBUTES_INIT_CONTEXT_TYPE(&Attr, IndirectMonitorContextWrapper);
    // IddCx owns the monitor object's lifetime; free our per-monitor context
    // when WDF destroys the object (after departure) to avoid a use-after-free.
    Attr.EvtCleanupCallback = [](WDFOBJECT Object)
    {
        auto* pWrapper = WdfObjectGet_IndirectMonitorContextWrapper(Object);
        if (pWrapper)
        {
            pWrapper->Cleanup();
        }
    };

    // EDID-less monitor: modes come from GetDefaultModes/QueryTargetModes,
    // which report the requested mode (preferred), the native size and the
    // standard 720p-4K list stored in / derived from the monitor context.
    IDDCX_MONITOR_INFO MonitorInfo = {};
    MonitorInfo.Size = sizeof(MonitorInfo);
    MonitorInfo.MonitorType = DISPLAYCONFIG_OUTPUT_TECHNOLOGY_HDMI;
    MonitorInfo.ConnectorIndex = (UINT) Slot;
    MonitorInfo.MonitorDescription.Size = sizeof(MonitorInfo.MonitorDescription);
    MonitorInfo.MonitorDescription.Type = IDDCX_MONITOR_DESCRIPTION_TYPE_EDID;
    MonitorInfo.MonitorDescription.DataSize = 0;
    MonitorInfo.MonitorDescription.pData = nullptr;
    CoCreateGuid(&MonitorInfo.MonitorContainerId);

    IDARG_IN_MONITORCREATE MonitorCreate = {};
    MonitorCreate.ObjectAttributes = &Attr;
    MonitorCreate.pMonitorInfo = &MonitorInfo;

    IDARG_OUT_MONITORCREATE MonitorCreateOut;
    NTSTATUS Status = IddCxMonitorCreate(m_Adapter, &MonitorCreate, &MonitorCreateOut);
    if (!NT_SUCCESS(Status))
    {
        m_Connectors->Release(Slot);
        return Status;
    }

    auto* pMonitorContextWrapper = WdfObjectGet_IndirectMonitorContextWrapper(MonitorCreateOut.MonitorObject);
    auto* pMonitorContext = new IndirectMonitorContext(MonitorCreateOut.MonitorObject);
    // From here the context's destructor (run by the WDF cleanup callback)
    // returns the slot, including on the arrival-failure path below.
    pMonitorContext->m_Pool = m_Connectors;
    pMonitorContext->m_ConnectorSlot = Slot;
    pMonitorContextWrapper->pContext = pMonitorContext;
    pMonitorContext->m_MonitorId = MonitorId;
    pMonitorContext->m_Width = Width;
    pMonitorContext->m_Height = Height;
    pMonitorContext->m_RefreshHz = RefreshHz;
    pMonitorContext->m_NativeWidth = NativeWidth;
    pMonitorContext->m_NativeHeight = NativeHeight;
    pMonitorContext->m_Export->OwnerSid = OwnerSid;

    IDARG_OUT_MONITORARRIVAL ArrivalOut;
    Status = IddCxMonitorArrival(MonitorCreateOut.MonitorObject, &ArrivalOut);
    if (!NT_SUCCESS(Status))
    {
        WdfObjectDelete(MonitorCreateOut.MonitorObject);
        return Status;
    }

    m_Monitors.push_back({ MonitorId, MonitorCreateOut.MonitorObject, Owner, pMonitorContext->m_Export });
    if (pMonitorId)
    {
        *pMonitorId = MonitorId;
    }
    return STATUS_SUCCESS;
}

bool IndirectDeviceContext::MayControl(const ActiveMonitor& m, WDFFILEOBJECT Requester, const vector<BYTE>& CallerSid, bool Privileged)
{
    if (Privileged || (m.Owner != nullptr && m.Owner == Requester))
    {
        return true;
    }
    // The device is open to every authenticated user, so another user's
    // process must not be able to tear down this user's monitors.
    const vector<BYTE>* OwnerSid = m.Export ? &m.Export->OwnerSid : nullptr;
    return OwnerSid && !OwnerSid->empty() && !CallerSid.empty() &&
        EqualSid((PSID) OwnerSid->data(), (PSID) CallerSid.data());
}

NTSTATUS IndirectDeviceContext::RemoveMonitor(DWORD MonitorId, WDFFILEOBJECT Requester, const vector<BYTE>& CallerSid, bool Privileged)
{
    IDDCX_MONITOR Monitor = nullptr;
    AcquireSRWLockExclusive(&m_Lock);
    for (auto it = m_Monitors.begin(); it != m_Monitors.end(); ++it)
    {
        if (it->Id == MonitorId)
        {
            if (!MayControl(*it, Requester, CallerSid, Privileged))
            {
                ReleaseSRWLockExclusive(&m_Lock);
                return STATUS_ACCESS_DENIED;
            }
            Monitor = it->Monitor;
            m_Monitors.erase(it);
            break;
        }
    }
    ReleaseSRWLockExclusive(&m_Lock);

    if (!Monitor)
    {
        return STATUS_NOT_FOUND;
    }
    // IddCxMonitorDeparture releases IddCx's reference; WDF then destroys
    // the monitor object (do not WdfObjectDelete it here). It runs outside
    // the lock because departure synchronously unassigns the swap chain.
    IddCxMonitorDeparture(Monitor);
    return STATUS_SUCCESS;
}

void IndirectDeviceContext::RemoveAllMonitors(WDFFILEOBJECT Requester, const vector<BYTE>& CallerSid, bool Privileged)
{
    vector<ActiveMonitor> Monitors;
    AcquireSRWLockExclusive(&m_Lock);
    for (auto it = m_Monitors.begin(); it != m_Monitors.end();)
    {
        if (MayControl(*it, Requester, CallerSid, Privileged))
        {
            Monitors.push_back(*it);
            it = m_Monitors.erase(it);
        }
        else
        {
            ++it;
        }
    }
    ReleaseSRWLockExclusive(&m_Lock);

    for (auto& m : Monitors)
    {
        IddCxMonitorDeparture(m.Monitor);
    }
}

void IndirectDeviceContext::RemoveMonitorsOwnedBy(WDFFILEOBJECT Owner)
{
    vector<ActiveMonitor> Removed;
    AcquireSRWLockExclusive(&m_Lock);
    for (auto it = m_Monitors.begin(); it != m_Monitors.end();)
    {
        // A monitor with no recorded owner keeps the old behaviour: any
        // closing handle removes it.
        if (it->Owner == Owner || it->Owner == nullptr)
        {
            Removed.push_back(*it);
            it = m_Monitors.erase(it);
        }
        else
        {
            // The capture helper that enabled export has gone (the host may
            // have fallen back to Desktop Duplication): stop the per-frame
            // copies. A new helper re-enables export with its own request.
            if (it->Export && it->Export->Requester == Owner)
            {
                InterlockedExchange(&it->Export->Requested, 0);
                InterlockedExchange(&it->Export->CursorRequested, 0);
                it->Export->Requester = nullptr;
            }
            ++it;
        }
    }
    ReleaseSRWLockExclusive(&m_Lock);

    for (auto& m : Removed)
    {
        IddCxMonitorDeparture(m.Monitor);
    }
}

NTSTATUS IndirectDeviceContext::GetFrameSurface(DWORD MonitorId, const vector<BYTE>& CallerSid, WDFFILEOBJECT Requester,
    shared_ptr<FrameExport>* pExport)
{
    AcquireSRWLockExclusive(&m_Lock);
    shared_ptr<FrameExport> Export;
    for (const auto& m : m_Monitors)
    {
        if (m.Id == MonitorId)
        {
            Export = m.Export;
            break;
        }
    }
    NTSTATUS Status = STATUS_SUCCESS;
    if (!Export)
    {
        Status = STATUS_NOT_FOUND;
    }
    else if (Export->OwnerSid.empty() || CallerSid.empty() ||
        !EqualSid((PSID) Export->OwnerSid.data(), (PSID) CallerSid.data()))
    {
        // Another user's monitor (or an unidentifiable caller): never
        // export it. The helper then exits and the host uses DDA.
        Status = STATUS_ACCESS_DENIED;
    }
    else
    {
        Export->Requester = Requester;
        InterlockedExchange(&Export->Requested, 1);
    }
    ReleaseSRWLockExclusive(&m_Lock);

    if (NT_SUCCESS(Status))
    {
        *pExport = move(Export);
    }
    return Status;
}

IndirectMonitorContext::IndirectMonitorContext(_In_ IDDCX_MONITOR Monitor) :
    m_Monitor(Monitor)
{
}

IndirectMonitorContext::~IndirectMonitorContext()
{
    m_ProcessingThread.reset();
    if (m_Pool)
    {
        m_Pool->Release(m_ConnectorSlot);
    }
}

void IndirectMonitorContext::AssignSwapChain(IDDCX_SWAPCHAIN SwapChain, LUID RenderAdapter, HANDLE NewFrameEvent)
{
    m_ProcessingThread.reset();

    shared_ptr<Direct3DDevice> Device;
    for (int Attempt = 0; Attempt < kDeviceInitAttempts; ++Attempt)
    {
        if (Attempt > 0)
        {
            Sleep(kDeviceInitRetryMs * Attempt);
        }
        auto Candidate = make_shared<Direct3DDevice>(RenderAdapter);
        if (SUCCEEDED(Candidate->Init()))
        {
            Device = Candidate;
            break;
        }
    }

    if (Device)
    {
        unique_ptr<SwapChainProcessor> Processor(new SwapChainProcessor(SwapChain, Device, NewFrameEvent, m_Monitor, m_MonitorId, m_Export));
        if (Processor->Started())
        {
            m_ProcessingThread = move(Processor);
            return;
        }
    }
    // Deleting the swap chain makes the OS assign a new one (possibly on
    // another render adapter), so the monitor recovers instead of freezing.
    WdfObjectDelete(SwapChain);
}

void IndirectMonitorContext::UnassignSwapChain()
{
    m_ProcessingThread.reset();
}

#pragma endregion

#pragma region DDI Callbacks

_Use_decl_annotations_
NTSTATUS DrawSplayAdapterInitFinished(IDDCX_ADAPTER AdapterObject, const IDARG_IN_ADAPTER_INIT_FINISHED* pInArgs)
{
    // No monitors are reported here; the host plugs them on demand via IOCTL.
    // Monitor creation is only allowed once initialization succeeded.
    NTSTATUS InitStatus = pInArgs->AdapterInitStatus;
    auto* pWrapper = WdfObjectGet_IndirectDeviceContextWrapper(AdapterObject);
    auto readContext = [pWrapper]() -> IndirectDeviceContext*
    {
        return pWrapper ? static_cast<IndirectDeviceContext*>(InterlockedCompareExchangePointer(
            reinterpret_cast<PVOID volatile*>(&pWrapper->pContext), nullptr, nullptr)) : nullptr;
    };

    if (auto* pContext = readContext())
    {
        pContext->SetAdapterInitStatus(InitStatus);
        return STATUS_SUCCESS;
    }

    // InitAdapter hasn't attached its context yet: park the status. Whoever
    // takes it out of the slot first (InitAdapter or this re-check) applies it.
    InterlockedExchange(&g_ParkedAdapterInitStatus, InitStatus);
    if (auto* pContext = readContext())
    {
        LONG Parked = InterlockedExchange(&g_ParkedAdapterInitStatus, kNoParkedStatus);
        if (Parked != kNoParkedStatus)
        {
            pContext->SetAdapterInitStatus(Parked);
        }
    }
    return STATUS_SUCCESS;
}

_Use_decl_annotations_
NTSTATUS DrawSplayAdapterCommitModes(IDDCX_ADAPTER AdapterObject, const IDARG_IN_COMMITMODES* pInArgs)
{
    UNREFERENCED_PARAMETER(AdapterObject);
    UNREFERENCED_PARAMETER(pInArgs);

    return STATUS_SUCCESS;
}

_Use_decl_annotations_
NTSTATUS DrawSplayParseMonitorDescription(const IDARG_IN_PARSEMONITORDESCRIPTION* pInArgs, IDARG_OUT_PARSEMONITORDESCRIPTION* pOutArgs)
{
    UNREFERENCED_PARAMETER(pInArgs);
    UNREFERENCED_PARAMETER(pOutArgs);

    // DrawSplay monitors are EDID-less; descriptions are never parsed.
    return STATUS_NOT_IMPLEMENTED;
}

_Use_decl_annotations_
NTSTATUS DrawSplayMonitorGetDefaultModes(IDDCX_MONITOR MonitorObject, const IDARG_IN_GETDEFAULTDESCRIPTIONMODES* pInArgs, IDARG_OUT_GETDEFAULTDESCRIPTIONMODES* pOutArgs)
{
    auto* pMonitorContextWrapper = WdfObjectGet_IndirectMonitorContextWrapper(MonitorObject);
    DWORD w = 1920, h = 1080, hz = 60, nw = 0, nh = 0;
    if (pMonitorContextWrapper && pMonitorContextWrapper->pContext)
    {
        w = pMonitorContextWrapper->pContext->m_Width;
        h = pMonitorContextWrapper->pContext->m_Height;
        hz = pMonitorContextWrapper->pContext->m_RefreshHz;
        nw = pMonitorContextWrapper->pContext->m_NativeWidth;
        nh = pMonitorContextWrapper->pContext->m_NativeHeight;
    }

    vector<IDDCX_MONITOR_MODE> Modes;
    for (const auto& s : MonitorResolutions(w, h, nw, nh))
    {
        for (DWORD rate : MonitorRefreshRates(hz))
        {
            Modes.push_back(CreateIddCxMonitorMode(s.Width, s.Height, rate, IDDCX_MONITOR_MODE_ORIGIN_DRIVER));
        }
    }

    pOutArgs->DefaultMonitorModeBufferOutputCount = (UINT) Modes.size();
    if (pInArgs->DefaultMonitorModeBufferInputCount == 0)
    {
        return STATUS_SUCCESS;
    }
    if (pInArgs->DefaultMonitorModeBufferInputCount < Modes.size())
    {
        return STATUS_BUFFER_TOO_SMALL;
    }

    copy(Modes.begin(), Modes.end(), pInArgs->pDefaultMonitorModes);
    // Index 0 is the initial mode at the tablet's refresh rate.
    pOutArgs->PreferredMonitorModeIdx = 0;
    return STATUS_SUCCESS;
}

_Use_decl_annotations_
NTSTATUS DrawSplayMonitorQueryModes(IDDCX_MONITOR MonitorObject, const IDARG_IN_QUERYTARGETMODES* pInArgs, IDARG_OUT_QUERYTARGETMODES* pOutArgs)
{
    auto* pMonitorContextWrapper = WdfObjectGet_IndirectMonitorContextWrapper(MonitorObject);
    DWORD w = 1920, h = 1080, hz = 60, nw = 0, nh = 0;
    if (pMonitorContextWrapper && pMonitorContextWrapper->pContext)
    {
        w = pMonitorContextWrapper->pContext->m_Width;
        h = pMonitorContextWrapper->pContext->m_Height;
        hz = pMonitorContextWrapper->pContext->m_RefreshHz;
        nw = pMonitorContextWrapper->pContext->m_NativeWidth;
        nh = pMonitorContextWrapper->pContext->m_NativeHeight;
    }

    // Every resolution at the tablet's rate plus a 60 Hz fallback, so the
    // user can pick 720p-4K (and the native panel size) in Display Settings.
    vector<IDDCX_TARGET_MODE> TargetModes;
    for (const auto& s : MonitorResolutions(w, h, nw, nh))
    {
        for (DWORD rate : MonitorRefreshRates(hz))
        {
            TargetModes.push_back(CreateIddCxTargetMode(s.Width, s.Height, rate));
        }
    }

    pOutArgs->TargetModeBufferOutputCount = (UINT) TargetModes.size();

    if (pInArgs->TargetModeBufferInputCount >= TargetModes.size())
    {
        copy(TargetModes.begin(), TargetModes.end(), pInArgs->pTargetModes);
    }

    return STATUS_SUCCESS;
}

_Use_decl_annotations_
NTSTATUS DrawSplayMonitorAssignSwapChain(IDDCX_MONITOR MonitorObject, const IDARG_IN_SETSWAPCHAIN* pInArgs)
{
    auto* pMonitorContextWrapper = WdfObjectGet_IndirectMonitorContextWrapper(MonitorObject);
    pMonitorContextWrapper->pContext->AssignSwapChain(pInArgs->hSwapChain, pInArgs->RenderAdapterLuid, pInArgs->hNextSurfaceAvailable);
    return STATUS_SUCCESS;
}

_Use_decl_annotations_
NTSTATUS DrawSplayMonitorUnassignSwapChain(IDDCX_MONITOR MonitorObject)
{
    auto* pMonitorContextWrapper = WdfObjectGet_IndirectMonitorContextWrapper(MonitorObject);
    pMonitorContextWrapper->pContext->UnassignSwapChain();
    return STATUS_SUCCESS;
}

// Runs while impersonating the requesting process: records its user SID.
static VOID CaptureCallerSid(WDFREQUEST Request, PVOID Context)
{
    UNREFERENCED_PARAMETER(Request);
    auto* Sid = static_cast<vector<BYTE>*>(Context);
    HANDLE Token = nullptr;
    if (!OpenThreadToken(GetCurrentThread(), TOKEN_QUERY, TRUE, &Token))
    {
        return;
    }
    BYTE Buffer[sizeof(TOKEN_USER) + SECURITY_MAX_SID_SIZE];
    DWORD Length = 0;
    if (GetTokenInformation(Token, TokenUser, Buffer, sizeof(Buffer), &Length))
    {
        PSID UserSid = reinterpret_cast<TOKEN_USER*>(Buffer)->User.Sid;
        const BYTE* p = static_cast<const BYTE*>(UserSid);
        Sid->assign(p, p + GetLengthSid(UserSid));
    }
    CloseHandle(Token);
}

// The user behind a control request, or empty when it cannot be determined
// (export is then refused, never granted).
static vector<BYTE> CallerSid(WDFREQUEST Request)
{
    vector<BYTE> Sid;
    if (!NT_SUCCESS(WdfRequestImpersonate(Request, SecurityImpersonation, CaptureCallerSid, &Sid)))
    {
        Sid.clear();
    }
    return Sid;
}

static VOID CapturePrivileged(WDFREQUEST Request, PVOID Context)
{
    UNREFERENCED_PARAMETER(Request);
    auto* Privileged = static_cast<bool*>(Context);
    HANDLE Token = nullptr;
    if (!OpenThreadToken(GetCurrentThread(), TOKEN_QUERY, TRUE, &Token))
    {
        return;
    }
    BYTE AdminSid[SECURITY_MAX_SID_SIZE];
    BYTE SystemSid[SECURITY_MAX_SID_SIZE];
    DWORD AdminLen = sizeof(AdminSid), SystemLen = sizeof(SystemSid);
    BOOL IsMember = FALSE;
    if (CreateWellKnownSid(WinBuiltinAdministratorsSid, nullptr, AdminSid, &AdminLen) &&
        CheckTokenMembership(Token, AdminSid, &IsMember) && IsMember)
    {
        *Privileged = true;
    }
    else if (CreateWellKnownSid(WinLocalSystemSid, nullptr, SystemSid, &SystemLen) &&
        CheckTokenMembership(Token, SystemSid, &IsMember) && IsMember)
    {
        *Privileged = true;
    }
    CloseHandle(Token);
}

// Is the caller an (elevated) administrator or SYSTEM? False when unknown.
static bool CallerPrivileged(WDFREQUEST Request)
{
    bool Privileged = false;
    if (!NT_SUCCESS(WdfRequestImpersonate(Request, SecurityImpersonation, CapturePrivileged, &Privileged)))
    {
        return false;
    }
    return Privileged;
}

_Use_decl_annotations_
VOID DrawSplayDeviceIoControl(WDFDEVICE Device, WDFREQUEST Request, size_t OutputBufferLength, size_t InputBufferLength, ULONG IoControlCode)
{
    auto* pContextWrapper = WdfObjectGet_IndirectDeviceContextWrapper(Device);
    IndirectDeviceContext* pContext = pContextWrapper ? pContextWrapper->pContext : nullptr;
    if (!pContext)
    {
        WdfRequestComplete(Request, STATUS_DEVICE_NOT_READY);
        return;
    }

    NTSTATUS Status = STATUS_INVALID_DEVICE_REQUEST;
    ULONG_PTR Information = 0;

    switch (IoControlCode)
    {
    case IOCTL_DRAWSPLAY_ADD_MONITOR:
    {
        if (InputBufferLength < DRAWSPLAY_ADD_MONITOR_IN_V1_SIZE ||
            OutputBufferLength < sizeof(DRAWSPLAY_ADD_MONITOR_OUT))
        {
            Status = STATUS_BUFFER_TOO_SMALL;
            break;
        }
        DRAWSPLAY_ADD_MONITOR_IN In = {};
        PVOID pRawIn = nullptr;
        size_t RawInLen = 0;
        DRAWSPLAY_ADD_MONITOR_OUT* pOut = nullptr;
        if (!NT_SUCCESS(WdfRequestRetrieveInputBuffer(Request, DRAWSPLAY_ADD_MONITOR_IN_V1_SIZE, &pRawIn, &RawInLen)) ||
            !NT_SUCCESS(WdfRequestRetrieveOutputBuffer(Request, sizeof(*pOut), (PVOID*)&pOut, nullptr)))
        {
            Status = STATUS_INVALID_PARAMETER;
            break;
        }
        // Accept both the v1 (12-byte) and v2 (with native size) layouts.
        memcpy(&In, pRawIn, min(RawInLen, sizeof(In)));
        DWORD MonitorId = 0;
        Status = pContext->AddMonitor(In.Width, In.Height, In.RefreshHz, In.NativeWidth, In.NativeHeight,
            WdfRequestGetFileObject(Request), CallerSid(Request), &MonitorId);
        if (NT_SUCCESS(Status))
        {
            pOut->MonitorId = MonitorId;
            Information = sizeof(*pOut);
        }
        break;
    }
    case IOCTL_DRAWSPLAY_REMOVE_MONITOR:
    {
        if (InputBufferLength < sizeof(DRAWSPLAY_REMOVE_MONITOR_IN))
        {
            Status = STATUS_BUFFER_TOO_SMALL;
            break;
        }
        DRAWSPLAY_REMOVE_MONITOR_IN* pIn = nullptr;
        if (!NT_SUCCESS(WdfRequestRetrieveInputBuffer(Request, sizeof(*pIn), (PVOID*)&pIn, nullptr)))
        {
            Status = STATUS_INVALID_PARAMETER;
            break;
        }
        Status = pContext->RemoveMonitor(pIn->MonitorId, WdfRequestGetFileObject(Request), CallerSid(Request),
            CallerPrivileged(Request));
        break;
    }
    case IOCTL_DRAWSPLAY_REMOVE_ALL:
    {
        pContext->RemoveAllMonitors(WdfRequestGetFileObject(Request), CallerSid(Request), CallerPrivileged(Request));
        Status = STATUS_SUCCESS;
        break;
    }
    case IOCTL_DRAWSPLAY_GET_FRAME_SURFACE:
    {
        if (InputBufferLength < DRAWSPLAY_GET_FRAME_SURFACE_IN_V1_SIZE ||
            OutputBufferLength < sizeof(DRAWSPLAY_GET_FRAME_SURFACE_OUT))
        {
            Status = STATUS_BUFFER_TOO_SMALL;
            break;
        }
        PVOID pRawIn = nullptr;
        size_t RawInLen = 0;
        DRAWSPLAY_GET_FRAME_SURFACE_OUT* pOut = nullptr;
        if (!NT_SUCCESS(WdfRequestRetrieveInputBuffer(Request, DRAWSPLAY_GET_FRAME_SURFACE_IN_V1_SIZE, &pRawIn, &RawInLen)) ||
            !NT_SUCCESS(WdfRequestRetrieveOutputBuffer(Request, sizeof(*pOut), (PVOID*)&pOut, nullptr)))
        {
            Status = STATUS_INVALID_PARAMETER;
            break;
        }
        // METHOD_BUFFERED: in and out share one buffer, so copy the input first.
        DRAWSPLAY_GET_FRAME_SURFACE_IN In = {};
        memcpy(&In, pRawIn, min(RawInLen, sizeof(In)));
        shared_ptr<FrameExport> Export;
        Status = pContext->GetFrameSurface(In.MonitorId, CallerSid(Request), WdfRequestGetFileObject(Request), &Export);
        if (!NT_SUCCESS(Status))
        {
            break;
        }
        if (In.Flags & DRAWSPLAY_FRAME_SURFACE_FLAG_HW_CURSOR)
        {
            InterlockedExchange(&Export->CursorRequested, 1);
        }
        DRAWSPLAY_GET_FRAME_SURFACE_OUT Out = {};
        // Read the generation first: the fields it publishes are written
        // before it is bumped.
        Out.Generation = (unsigned int) InterlockedCompareExchange(&Export->Generation, 0, 0);
        MemoryBarrier();
        Out.Width = Export->Width;
        Out.Height = Export->Height;
        Out.Format = Export->Format;
        Out.AdapterLuidLow = Export->AdapterLuid.LowPart;
        Out.AdapterLuidHigh = Export->AdapterLuid.HighPart;
        Out.FramesPublished = (unsigned int) InterlockedCompareExchange(&Export->FramesPublished, 0, 0);
        Out.FramesSkipped = (unsigned int) InterlockedCompareExchange(&Export->FramesSkipped, 0, 0);
        Out.Failed = (unsigned int) InterlockedCompareExchange(&Export->Failed, 0, 0);
        Out.HardwareCursor = (unsigned int) InterlockedCompareExchange(&Export->HardwareCursor, 0, 0);
        *pOut = Out;
        Information = sizeof(*pOut);
        break;
    }
    default:
        break;
    }

    WdfRequestCompleteWithInformation(Request, Status, Information);
}

#pragma endregion
