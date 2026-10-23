package com.drawsplay.wifiprobe;

import android.app.Activity;
import android.content.Context;
import android.net.ConnectivityManager;
import android.net.LinkAddress;
import android.net.LinkProperties;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.nsd.NsdManager;
import android.net.nsd.NsdServiceInfo;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.util.Log;
import android.widget.TextView;
import java.io.IOException;
import java.net.Inet4Address;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/** Isolated, finite NSD probe. It cannot receive video or inject input. */
public final class DiscoveryProbeActivity extends Activity {
    private static final String TAG = "DS-WiFiProbe";
    private final Handler handler = new Handler(Looper.getMainLooper());
    private final ExecutorService worker = Executors.newSingleThreadExecutor();
    private final Object guard = new Object();
    private final Runnable timeout = () -> finish();
    private ServerSocket input;
    private ServerSocket video;
    private NsdManager nsd;
    private NsdManager.RegistrationListener registration;
    private WifiManager.MulticastLock multicast;
    private boolean stopped;
    private TextView status;

    @Override
    public void onCreate(Bundle state) {
        super.onCreate(state);
        status = new TextView(this);
        status.setText("Temporary Wi-Fi discovery probe. No screen or input access.\nStarting...");
        setContentView(status);
        handler.postDelayed(timeout, 40000);
        final String address = getIntent().getStringExtra("address");
        worker.execute(() -> {
            try {
                startProbe(address);
            } catch (Exception error) {
                Log.e(TAG, "PROBE_FAILED", error);
                runOnUiThread(() -> {
                    status.setText("Wi-Fi probe failed: " + error.getMessage());
                    finish();
                });
            }
        });
    }

    private void startProbe(String literal) throws IOException {
        if (Build.VERSION.SDK_INT < 33) {
            throw new IOException("This scoped NSD experiment requires API 33+; no unscoped fallback");
        }
        if (literal == null || !literal.matches("[0-9]{1,3}(\\.[0-9]{1,3}){3}")) {
            throw new IOException("Provide the tablet's numeric Wi-Fi IPv4 address");
        }
        String[] parts = literal.split("\\.");
        byte[] bytes = new byte[4];
        for (int index = 0; index < 4; index++) {
            int octet = Integer.parseInt(parts[index]);
            if (octet > 255) throw new IOException("Invalid IPv4 octet");
            bytes[index] = (byte) octet;
        }
        InetAddress address = InetAddress.getByAddress(bytes);
        if (!(address instanceof Inet4Address) || address.isLoopbackAddress()
                || address.isAnyLocalAddress() || address.isMulticastAddress()) {
            throw new IOException("Require a usable selected Wi-Fi IPv4 address");
        }
        ConnectivityManager connectivity =
            (ConnectivityManager) getSystemService(Context.CONNECTIVITY_SERVICE);
        Network selected = null;
        for (Network network : connectivity.getAllNetworks()) {
            NetworkCapabilities caps = connectivity.getNetworkCapabilities(network);
            LinkProperties links = connectivity.getLinkProperties(network);
            if (caps == null || links == null
                    || !caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)
                    || caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) continue;
            for (LinkAddress link : links.getLinkAddresses()) {
                if (link.getAddress().equals(address)) selected = network;
            }
        }
        if (selected == null) throw new IOException("Address is not assigned to a Wi-Fi Network");
        synchronized (guard) {
            if (stopped) return;
            input = new ServerSocket();
            input.bind(new InetSocketAddress(address, 0), 1);
            video = new ServerSocket();
            video.bind(new InetSocketAddress(address, 0), 1);
            WifiManager wifi = (WifiManager) getApplicationContext().getSystemService(Context.WIFI_SERVICE);
            multicast = wifi.createMulticastLock("drawsplay-isolated-wifi-probe");
            multicast.setReferenceCounted(false);
            multicast.acquire();
            NsdServiceInfo info = new NsdServiceInfo();
            info.setServiceName("DrawSplay Wi-Fi test");
            info.setServiceType("_drawsplay-tablet._tcp.");
            info.setPort(input.getLocalPort());
            info.setAttribute("v", "2");
            info.setAttribute("videoPort", Integer.toString(video.getLocalPort()));
            info.setAttribute("probe", "1");
            info.setNetwork(selected);
            nsd = (NsdManager) getSystemService(Context.NSD_SERVICE);
            final int videoPort = video.getLocalPort();
            registration = new NsdManager.RegistrationListener() {
                @Override public void onServiceRegistered(NsdServiceInfo service) {
                    Log.i(TAG, "REGISTERED " + service.getServiceName()
                        + " input=" + info.getPort() + " video=" + videoPort);
                    runOnUiThread(() -> status.setText("Advertising a temporary tablet on selected Wi-Fi.\n"
                        + "Host may discover it; no data is accepted.\nCloses automatically after 40 seconds."));
                }
                @Override public void onRegistrationFailed(NsdServiceInfo service, int code) {
                    Log.e(TAG, "REGISTRATION_FAILED code=" + code);
                    runOnUiThread(() -> finish());
                }
                @Override public void onServiceUnregistered(NsdServiceInfo service) {
                    Log.i(TAG, "UNREGISTERED");
                }
                @Override public void onUnregistrationFailed(NsdServiceInfo service, int code) {
                    Log.e(TAG, "UNREGISTRATION_FAILED code=" + code);
                }
            };
            nsd.registerService(info, NsdManager.PROTOCOL_DNS_SD, registration);
        }
    }

    @Override
    protected void onDestroy() {
        handler.removeCallbacks(timeout);
        synchronized (guard) {
            stopped = true;
            if (nsd != null && registration != null) {
                try {
                    nsd.unregisterService(registration);
                } catch (IllegalArgumentException error) {
                    Log.w(TAG, "Probe registration already absent", error);
                }
            }
            close(input);
            close(video);
            if (multicast != null && multicast.isHeld()) multicast.release();
        }
        worker.shutdownNow();
        Log.i(TAG, "STOPPED sockets closed and multicast lock released");
        super.onDestroy();
    }

    private void close(ServerSocket socket) {
        if (socket == null) return;
        try {
            socket.close();
        } catch (IOException error) {
            Log.e(TAG, "Probe socket close failed", error);
        }
    }
}
