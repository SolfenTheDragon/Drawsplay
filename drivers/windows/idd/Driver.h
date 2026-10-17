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
#include <wrl.h>

#include <memory>
#include <vector>

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
        /// Consumes buffers from an indirect display swap-chain object.
        /// </summary>
        class SwapChainProcessor
        {
        public:
            SwapChainProcessor(IDDCX_SWAPCHAIN hSwapChain, std::shared_ptr<Direct3DDevice> Device, HANDLE NewFrameEvent);
            ~SwapChainProcessor();

            // False when the processing thread could not be created; the
            // caller still owns the swap chain then.
            bool Started() const { return m_hThread.IsValid(); }

        private:
            static DWORD CALLBACK RunThread(LPVOID Argument);

            void Run();
            void RunCore();

            IDDCX_SWAPCHAIN m_hSwapChain;
            std::shared_ptr<Direct3DDevice> m_Device;
            HANDLE m_hAvailableBufferEvent;
            Microsoft::WRL::Wrappers::Thread m_hThread;
            Microsoft::WRL::Wrappers::Event m_hTerminateEvent;
        };

        /// <summary>
        /// A single dynamically-plugged virtual monitor and its requested mode.
        /// </summary>
        struct IndirectMonitorContext
        {
            IndirectMonitorContext(_In_ IDDCX_MONITOR Monitor);
            ~IndirectMonitorContext();

            void AssignSwapChain(IDDCX_SWAPCHAIN SwapChain, LUID RenderAdapter, HANDLE NewFrameEvent);
            void UnassignSwapChain();

            IDDCX_MONITOR m_Monitor;
            DWORD m_MonitorId = 0;
            DWORD m_Width = 1920;
            DWORD m_Height = 1080;
            DWORD m_RefreshHz = 60;
            DWORD m_NativeWidth = 0;
            DWORD m_NativeHeight = 0;
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
            NTSTATUS AddMonitor(DWORD Width, DWORD Height, DWORD RefreshHz, DWORD NativeWidth, DWORD NativeHeight, _Out_ DWORD* pMonitorId);
            NTSTATUS RemoveMonitor(DWORD MonitorId);
            void RemoveAllMonitors();

            IDDCX_ADAPTER m_Adapter;

        protected:
            NTSTATUS AddMonitorLocked(DWORD Width, DWORD Height, DWORD RefreshHz, DWORD NativeWidth, DWORD NativeHeight, _Out_ DWORD* pMonitorId);

            WDFDEVICE m_WdfDevice;
            DWORD m_NextMonitorId = 1;
            SRWLOCK m_Lock = SRWLOCK_INIT;
            // Monitors can only be created once IddCx reports the adapter
            // initialized (EvtIddCxAdapterInitFinished); until then the host
            // gets STATUS_DEVICE_NOT_READY and retries.
            volatile LONG m_AdapterReady = 0;

            struct ActiveMonitor
            {
                DWORD Id;
                IDDCX_MONITOR Monitor;
            };
            std::vector<ActiveMonitor> m_Monitors;
        };
    }
}
