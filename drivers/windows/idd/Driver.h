// Driver.h - DrawSplay IddCx indirect display driver.
//
// Derived from the Microsoft IddSampleDriver (MS-PL); see LICENSE. The
// sample's static three-monitor model is replaced by a dynamic one: the
// host plugs and unplugs a single virtual monitor on demand via custom
// IOCTLs (Public.h), each created with the exact resolution and refresh
// rate of the connected tablet.
#pragma once

#define NOMINMAX
#include <windows.h>
#include <bugcodes.h>
#include <wudfwdm.h>
#include <wdf.h>
#include <iddcx.h>

#include <dxgi1_5.h>
#include <d3d11_2.h>
#include <avrt.h>
#include <sddl.h>
#include <wrl.h>

#include <memory>
#include <vector>

// Defined in Public.h (shared with the host and helper).
struct _DRAWSPLAY_CURSOR_SHARED;

namespace Microsoft
{
    namespace WRL
    {
        namespace Wrappers
        {
            typedef HandleT<HandleTraits::HANDLENullTraits> Thread;
        }
    }
}

namespace Microsoft
{
    namespace IndirectDisp
    {
        /// <summary>
        /// Manages the creation and lifetime of a Direct3D render device.
        /// </summary>
        struct Direct3DDevice
        {
            Direct3DDevice(LUID AdapterLuid);
            Direct3DDevice();
            HRESULT Init();

            LUID AdapterLuid;
            D3D_FEATURE_LEVEL FeatureLevel = D3D_FEATURE_LEVEL_9_1;
            Microsoft::WRL::ComPtr<IDXGIFactory5> DxgiFactory;
            Microsoft::WRL::ComPtr<IDXGIAdapter1> Adapter;
            Microsoft::WRL::ComPtr<ID3D11Device> Device;
            Microsoft::WRL::ComPtr<ID3D11DeviceContext> DeviceContext;
        };

        /// <summary>
        /// Per-monitor frame-export state, shared between the IOCTL path
        /// (which enables export and reads the published surface info) and
        /// the swap-chain thread (which creates the shared texture and copies
        /// frames into it). Plain fields are published before Generation is
        /// bumped with an interlocked write, so a reader that sees a new
        /// generation also sees its size, format and adapter.
        /// </summary>
        struct FrameExport
        {
            volatile LONG Requested = 0;
            volatile LONG Generation = 0;
            volatile LONG Failed = 0;
            volatile LONG FramesPublished = 0;
            volatile LONG FramesSkipped = 0;
            UINT Width = 0;
            UINT Height = 0;
            UINT Format = 0;
            LUID AdapterLuid = {};

            // Hardware cursor hand-off (DRAWSPLAY_FRAME_SURFACE_FLAG_HW_CURSOR).
            volatile LONG CursorRequested = 0;
            volatile LONG HardwareCursor = 0;
            // Named shared block (Public.h DRAWSPLAY_CURSOR_SHARED), created
            // by the swap-chain thread on first use; lives with the monitor.
            HANDLE CursorMapping = nullptr;
            ::_DRAWSPLAY_CURSOR_SHARED* Cursor = nullptr;

            // User (SID) who created the monitor. Only this user may enable
            // export, and the shared texture and cursor block are shared
            // with this user (and SYSTEM) only. Set once at creation.
            std::vector<BYTE> OwnerSid;
            // Control handle that enabled export; export is switched off
            // when it closes, so an exited helper costs no frame copies.
            WDFFILEOBJECT volatile Requester = nullptr;

            ~FrameExport();
        };

        /// <summary>
        /// Consumes buffers from an indirect display swap-chain object.
        /// </summary>
        class SwapChainProcessor
        {
        public:
            SwapChainProcessor(IDDCX_SWAPCHAIN hSwapChain, std::shared_ptr<Direct3DDevice> Device, HANDLE NewFrameEvent,
                IDDCX_MONITOR Monitor, DWORD MonitorId, std::shared_ptr<FrameExport> Export);
            ~SwapChainProcessor();

            // False when the processing thread could not be created; the
            // caller still owns the swap chain then.
            bool Started() const { return m_hThread.IsValid(); }

        private:
            static DWORD CALLBACK RunThread(LPVOID Argument);

            void Run();
            void RunCore();

            // Copies one acquired swap-chain surface into the shared texture
            // when export is enabled (see Public.h for the hand-off protocol).
            void ExportFrame(IDXGIResource* pSurface);
            bool EnsureSharedTexture(const D3D11_TEXTURE2D_DESC& SourceDesc);
            void ReleaseSharedTexture();

            // Hardware cursor: enabled once per swap chain on request, then
            // every cursor change is published to the shared block.
            void SetupHardwareCursor();
            void PublishCursor();
            void HideSharedCursor();

            // A frame that arrived while the consumer held the shared
            // texture is kept here and published as soon as the texture is
            // free, so the newest picture (e.g. the end of a stroke) is never
            // lost when nothing else changes on screen afterwards.
            void FlushPendingFrame();

            IDDCX_SWAPCHAIN m_hSwapChain;
            std::shared_ptr<Direct3DDevice> m_Device;
            HANDLE m_hAvailableBufferEvent;
            Microsoft::WRL::Wrappers::Thread m_hThread;
            Microsoft::WRL::Wrappers::Event m_hTerminateEvent;

            IDDCX_MONITOR m_Monitor;
            DWORD m_MonitorId;
            std::shared_ptr<FrameExport> m_Export;
            bool m_CursorSetupTried = false;
            Microsoft::WRL::Wrappers::Event m_hCursorEvent;
            std::vector<BYTE> m_CursorBuffer;
            DWORD m_LastShapeId = (DWORD) -1;
            Microsoft::WRL::ComPtr<ID3D11Texture2D> m_SharedTexture;
            Microsoft::WRL::ComPtr<IDXGIKeyedMutex> m_SharedMutex;
            // The named NT handle keeps the texture's name alive for consumers.
            Microsoft::WRL::Wrappers::HandleT<Microsoft::WRL::Wrappers::HandleTraits::HANDLENullTraits> m_SharedHandle;
            D3D11_TEXTURE2D_DESC m_SharedDesc = {};
            Microsoft::WRL::ComPtr<ID3D11Texture2D> m_PendingTexture;
            bool m_FramePending = false;
        };

        /// <summary>
        /// A single dynamically-plugged virtual monitor and its requested mode.
        /// </summary>
        /// <summary>
        /// IddCx connector slots (0 .. MaxMonitorsSupported-1). A slot is taken
        /// when a monitor is created and given back only once the monitor object
        /// is destroyed, so unplug/replug cycles reuse slots instead of burning
        /// a new connector index each time.
        /// </summary>
        struct ConnectorPool
        {
            volatile LONG Mask = 0;

            // Returns the lowest free slot, or -1 when all are in use.
            int Acquire(DWORD MaxSlots)
            {
                for (;;)
                {
                    LONG Old = Mask;
                    int Slot = -1;
                    for (DWORD i = 0; i < MaxSlots; ++i)
                    {
                        if (!(Old & (1L << i))) { Slot = (int) i; break; }
                    }
                    if (Slot < 0) return -1;
                    if (InterlockedCompareExchange(&Mask, Old | (1L << Slot), Old) == Old) return Slot;
                }
            }
            void Release(int Slot)
            {
                if (Slot >= 0) InterlockedAnd(&Mask, ~(1L << Slot));
            }
        };

        struct IndirectMonitorContext
        {
            IndirectMonitorContext(_In_ IDDCX_MONITOR Monitor);
            ~IndirectMonitorContext();

            std::shared_ptr<ConnectorPool> m_Pool;
            int m_ConnectorSlot = -1;

            void AssignSwapChain(IDDCX_SWAPCHAIN SwapChain, LUID RenderAdapter, HANDLE NewFrameEvent);
            void UnassignSwapChain();

            IDDCX_MONITOR m_Monitor;
            DWORD m_MonitorId = 0;
            DWORD m_Width = 1920;
            DWORD m_Height = 1080;
            DWORD m_RefreshHz = 60;
            DWORD m_NativeWidth = 0;
            DWORD m_NativeHeight = 0;
            std::shared_ptr<FrameExport> m_Export = std::make_shared<FrameExport>();
            std::unique_ptr<SwapChainProcessor> m_ProcessingThread;
        };

        /// <summary>
        /// The IddCx adapter. Plugs/unplugs monitors on host request.
        /// </summary>
        class IndirectDeviceContext
        {
        public:
            IndirectDeviceContext(_In_ WDFDEVICE WdfDevice);
            virtual ~IndirectDeviceContext();

            void InitAdapter();
            void SetAdapterInitStatus(NTSTATUS Status);

            // Host-driven monitor lifecycle. m_Lock serializes it against
            // control-handle cleanup, which can run concurrently.
            // Owner is the control handle (file object) that created the
            // monitor; closing that handle removes it (RemoveMonitorsOwnedBy).
            NTSTATUS AddMonitor(DWORD Width, DWORD Height, DWORD RefreshHz, DWORD NativeWidth, DWORD NativeHeight, WDFFILEOBJECT Owner,
                const std::vector<BYTE>& OwnerSid, _Out_ DWORD* pMonitorId);
            NTSTATUS RemoveMonitor(DWORD MonitorId);
            void RemoveAllMonitors();
            // Called when a control handle closes: removes the monitors it
            // created and switches off export it enabled.
            void RemoveMonitorsOwnedBy(WDFFILEOBJECT Owner);

            // Enables frame export for a monitor and reports its surface.
            // Only the user who created the monitor (CallerSid) may do so.
            NTSTATUS GetFrameSurface(DWORD MonitorId, const std::vector<BYTE>& CallerSid, WDFFILEOBJECT Requester,
                _Out_ std::shared_ptr<FrameExport>* pExport);

            IDDCX_ADAPTER m_Adapter;

        protected:
            NTSTATUS AddMonitorLocked(DWORD Width, DWORD Height, DWORD RefreshHz, DWORD NativeWidth, DWORD NativeHeight, WDFFILEOBJECT Owner,
                const std::vector<BYTE>& OwnerSid, _Out_ DWORD* pMonitorId);

            WDFDEVICE m_WdfDevice;
            DWORD m_NextMonitorId = 1;
            std::shared_ptr<ConnectorPool> m_Connectors = std::make_shared<ConnectorPool>();
            SRWLOCK m_Lock = SRWLOCK_INIT;
            // Monitors can only be created once IddCx reports the adapter
            // initialized (EvtIddCxAdapterInitFinished); until then the host
            // gets STATUS_DEVICE_NOT_READY and retries.
            volatile LONG m_AdapterReady = 0;

            struct ActiveMonitor
            {
                DWORD Id;
                IDDCX_MONITOR Monitor;
                WDFFILEOBJECT Owner;
                std::shared_ptr<FrameExport> Export;
            };
            std::vector<ActiveMonitor> m_Monitors;
        };
    }
}
