"use strict";
const $ = (id) => document.getElementById(id);
let autostartOn = false;
let serviceBusy = false;
let csrfToken = "";
let csrfPromise = null;
let debugConsoleEnabled = false;
let debugLogNext = 0;
let debugLogLines = [];
let currentCalibrationDevice = "";
let loadedSettings = null;
let resolutionManagedByOS = false;
const featureFlags = { wifi: false };
const DEBUG_LOG_MAX_LINES = 600;
const THEME_STORAGE_KEY = "drawsplay-theme";
const CUSTOM_RESOLUTION = "custom";
const FPS_DEFAULT = 120;
const RESOLUTION_PRESETS = new Set([
  "1280x720",
  "1600x900",
  "1680x1050",
  "1920x1080",
  "2560x1440",
  "2736x1824",
  "2880x1800",
  "3200x1800",
  "3840x2160",
]);
const resolutionBaseLabels = new Map();
let activeNativeResolutionKey = "";
let nativeResolutionWasCreated = false;

function captureResolutionBaseLabels() {
  const select = $("resolution-preset");
  for (const opt of select.options) {
    resolutionBaseLabels.set(opt.value, opt.textContent);
  }
}

function ensureNativeResolutionOption(nativeWidth, nativeHeight) {
  const select = $("resolution-preset");
  const hasNative = Number.isInteger(nativeWidth) && Number.isInteger(nativeHeight) &&
    nativeWidth > 0 && nativeHeight > 0;
  const nativeKey = hasNative ? `${nativeWidth}x${nativeHeight}` : "";

  if (activeNativeResolutionKey) {
    const prior = select.querySelector(`option[value="${activeNativeResolutionKey}"]`);
    if (prior && resolutionBaseLabels.has(activeNativeResolutionKey)) {
      prior.textContent = resolutionBaseLabels.get(activeNativeResolutionKey);
    }
    if (nativeResolutionWasCreated && prior) {
      prior.remove();
    }
  }

  activeNativeResolutionKey = "";
  nativeResolutionWasCreated = false;
  if (!nativeKey || nativeKey === CUSTOM_RESOLUTION) return;

  let option = select.querySelector(`option[value="${nativeKey}"]`);
  if (!option) {
    option = document.createElement("option");
    option.value = nativeKey;
    const customOption = select.querySelector(`option[value="${CUSTOM_RESOLUTION}"]`);
    if (customOption) {
      select.insertBefore(option, customOption);
    } else {
      select.appendChild(option);
    }
    nativeResolutionWasCreated = true;
  }
  const baseLabel = resolutionBaseLabels.get(nativeKey) || `${nativeWidth} × ${nativeHeight}`;
  option.textContent = `${baseLabel} (Native)`;
  activeNativeResolutionKey = nativeKey;
}

function setCustomResolutionVisibility() {
  $("resolution-custom-fields").classList.toggle(
    "visible",
    $("resolution-preset").value === CUSTOM_RESOLUTION
  );
}

function syncResolutionPreset(width, height) {
  const key = `${width}x${height}`;
  const select = $("resolution-preset");
  const hasOption = !!select.querySelector(`option[value="${key}"]`);
  select.value = (RESOLUTION_PRESETS.has(key) || hasOption) ? key : CUSTOM_RESOLUTION;
  $("width").value = width;
  $("height").value = height;
  setCustomResolutionVisibility();
}

function selectedResolutionDimensions() {
  const preset = $("resolution-preset").value;
  if (preset === CUSTOM_RESOLUTION) {
    return {
      width: parseInt($("width").value, 10),
      height: parseInt($("height").value, 10),
    };
  }
  const parts = preset.split("x");
  return {
    width: parseInt(parts[0], 10),
    height: parseInt(parts[1], 10),
  };
}

function availableFrameratesForResolution(width, height) {
  const pixels = width * height;
  if (!Number.isFinite(pixels) || pixels <= 0) return [30, 60];
  if (pixels <= 1920 * 1080) return [30, 60, 90, 120];
  if (pixels <= 2560 * 1600) return [30, 60, 90];
  if (pixels <= 3840 * 2160) return [30, 60];
  return [30];
}

function chooseFramerate(requested, available) {
  if (!Array.isArray(available) || available.length === 0) return FPS_DEFAULT;
  if (available.includes(requested)) return requested;
  let best = available[0];
  let bestDelta = Math.abs(requested - best);
  for (const fps of available) {
    const delta = Math.abs(requested - fps);
    if (delta < bestDelta || (delta === bestDelta && fps < best)) {
      best = fps;
      bestDelta = delta;
    }
  }
  return best;
}

function refreshFramerateOptions(preferredFPS) {
  const fpsSelect = $("fps");
  const { width, height } = selectedResolutionDimensions();
  const options = availableFrameratesForResolution(width, height);
  const current = Number.parseInt(preferredFPS, 10) || Number.parseInt(fpsSelect.value, 10) || FPS_DEFAULT;
  const chosen = chooseFramerate(current, options);

  fpsSelect.innerHTML = "";
  for (const fps of options) {
    const opt = document.createElement("option");
    opt.value = String(fps);
    opt.textContent = `${fps} fps`;
    fpsSelect.appendChild(opt);
  }
  fpsSelect.value = String(chosen);
}

function applyTheme(theme) {
  const isLight = theme === "light";
  document.documentElement.setAttribute("data-theme", isLight ? "light" : "dark");
  const toggle = $("theme-mode-toggle");
  const label = $("theme-label");
  if (toggle) toggle.checked = isLight;
  if (label) label.textContent = isLight ? "Light mode" : "Dark mode";
}

function applyPlatformVisibility(hostOS) {
  const showWindowsOnly = hostOS === "windows";
  for (const el of document.querySelectorAll("[data-windows-only]")) {
    el.hidden = !showWindowsOnly;
  }
}

function applyFeatureVisibility(flags) {
  const wifiEnabled = !!(flags && flags.wifi);
  featureFlags.wifi = wifiEnabled;
  for (const el of document.querySelectorAll("[data-feature-wifi]")) {
    el.hidden = !wifiEnabled;
  }
}

function applyResolutionPolicy(status) {
  const hostOS = String(status?.hostOS || "").toLowerCase();
  const platform = String(status?.activeClient?.platform || "").toLowerCase();
  const clientType = String(status?.activeClient?.type || "").toLowerCase();
  const isTabletClient = platform === "android" || clientType === "tablet";
  // Windows: DrawSplay follows whatever Windows Display Settings applies
  // (host has no independent resolution control there), so both the
  // resolution and FPS controls are hidden.
  const windowsManaged = hostOS === "windows" && isTabletClient;
  // Linux: the virtual monitor now declares a real, GNOME-selectable mode
  // list (see mutter_extend_linux.go's RecordVirtual "modes" property), so the
  // user picks resolution from Settings -> Displays like any other
  // monitor. Only resolution is host-managed here -- FPS is independent
  // of the display's refresh rate on Linux, so the FPS control stays.
  // Displays the desktop offers no other resolution for (GNOME 49 and
  // older, KDE Plasma before 6.7) take the configured one instead.
  const hostResolution = String(status?.video?.resolutionControl || "") === "host";
  const linuxManaged = hostOS === "linux" && isTabletClient && !hostResolution;
  const managed = windowsManaged || linuxManaged;
  resolutionManagedByOS = managed;

  const controls = $("resolution-controls");
  const fpsRow = $("fps-control-row");
  const hint = $("resolution-policy-hint");
  const linuxHint = $("resolution-policy-hint-linux");
  if (controls) {
    controls.hidden = managed;
    controls.style.display = managed ? "none" : "";
  }
  if (fpsRow) {
    fpsRow.hidden = windowsManaged;
    fpsRow.style.display = windowsManaged ? "none" : "";
  }
  if (hint) hint.hidden = !windowsManaged;
  if (linuxHint) linuxHint.hidden = !linuxManaged;
}

function linuxSetupPrompt(warnings, hostOS) {
  if (hostOS !== "linux") return "";
  if (!Array.isArray(warnings) || warnings.length === 0) return "";
  const lowerWarnings = warnings.map((w) => String(w).toLowerCase());
  const hasUinputWarning = lowerWarnings.some((w) => w.includes("/dev/uinput"));
  const hasADBPermissionWarning = lowerWarnings.some((w) => w.includes("usb permissions") || w.includes("no permissions"));
  const hasPipewireWarning = lowerWarnings.some((w) => w.includes("pipewire") || w.includes("gstreamer"));
  if (!hasUinputWarning && !hasADBPermissionWarning && !hasPipewireWarning) return "";
  const hints = [];
  if (hasADBPermissionWarning) hints.push("install Android udev rules and reconnect USB");
  if (hasUinputWarning) hints.push("enable /dev/uinput write access and map DrawSplay Pen to the virtual monitor");
  if (hasPipewireWarning) hints.push("install GStreamer PipeWire packages for Wayland capture");
  return `Linux setup required: ${hints.join("; ")}. This prompt disappears once setup is done.`;
}

function initializeTheme() {
  let theme = "dark";
  try {
    const stored = localStorage.getItem(THEME_STORAGE_KEY);
    if (stored === "light" || stored === "dark") {
      theme = stored;
    }
  } catch (_e) {
    // Ignore storage failures and keep dark default.
  }
  applyTheme(theme);
}

function setDebugConsoleEnabled(enabled) {
  debugConsoleEnabled = !!enabled;
  $("debug-panel").hidden = !debugConsoleEnabled;
  if (!debugConsoleEnabled) return;
  refreshDebugConsole();
}

function resetDebugConsole() {
  debugLogLines = [];
  debugLogNext = 0;
  $("debug-console").textContent = "";
}

function appendDebugLines(lines) {
  if (!Array.isArray(lines) || lines.length === 0) return;
  debugLogLines.push(...lines.map((line) => String(line)));
  if (debugLogLines.length > DEBUG_LOG_MAX_LINES) {
    debugLogLines = debugLogLines.slice(debugLogLines.length - DEBUG_LOG_MAX_LINES);
  }
  const consoleEl = $("debug-console");
  consoleEl.textContent = debugLogLines.join("\n");
}

async function refreshDebugConsole() {
  if (!debugConsoleEnabled) return;
  try {
    const body = await api(`/api/logs?since=${debugLogNext}`);
    const next = Number.parseInt(body.next, 10);
    if (Number.isFinite(next) && next >= 0) {
      debugLogNext = next;
    }
    appendDebugLines(body.lines);
  } catch (_e) {
    // Status polling already surfaces host reachability.
  }
}

async function ensureCSRF() {
  if (csrfToken) return csrfToken;
  if (!csrfPromise) {
    csrfPromise = fetch("/api/csrf", { cache: "no-store" })
      .then((res) => {
        if (!res.ok) throw new Error("Unable to initialize local API security");
        return res.json();
      })
      .then((body) => {
        csrfToken = body.token || "";
        if (!csrfToken) throw new Error("Local API did not provide a CSRF token");
        return csrfToken;
      })
      .catch((err) => {
        csrfPromise = null;
        throw err;
      });
  }
  return csrfPromise;
}

function resetCSRF() {
  csrfToken = "";
  csrfPromise = null;
}

async function api(path, opts) {
  const base = { ...(opts || {}) };
  const method = (base.method || "GET").toUpperCase();
  for (let attempt = 0; attempt < 2; attempt++) {
    const request = { ...base };
    if (method !== "GET" && method !== "HEAD") {
      const token = await ensureCSRF();
      request.headers = { ...(base.headers || {}), "X-DrawSplay-CSRF": token };
    }
    const res = await fetch(path, request);
    const body = await res.json().catch(() => ({}));
    if (res.ok) return body;
    const errText = String(body.error || res.statusText || "");
    const csrfMismatch = res.status === 403 &&
      /csrf token/i.test(errText) &&
      method !== "GET" &&
      method !== "HEAD";
    if (csrfMismatch && attempt === 0) {
      resetCSRF();
      continue;
    }
    throw new Error(errText || "Request failed");
  }
}

async function loadSettings() {
  const s = await api("/api/settings");
  loadedSettings = s;
  syncResolutionPreset(s.width, s.height);
  refreshFramerateOptions(s.fps);
  $("ffmpeg").value = s.ffmpeg;
  $("adb").value = s.adb;
  $("adb-serial").value = s.preferredADBSerial || "";
  $("wifi-enabled").checked = !!s.wifiEnabled;
  $("wifi-service-name").value = s.wifiServiceName || "DrawSplay";
  $("wifi-bind-address").value = s.wifiBindAddress || "0.0.0.0";
  $("wifi-input-port").value = s.wifiInputPort || 37183;
  $("wifi-video-port").value = s.wifiVideoPort || 37184;
  $("wifi-max-bitrate").value = s.wifiMaxBitrate || "12M";
  const wifiAuthConfigured = !!s.wifiAuthTokenConfigured;
  $("wifi-auth-token").value = "";
  $("wifi-auth-token").placeholder = wifiAuthConfigured
    ? "configured (enter new token to rotate)"
    : "shared token";
  $("wifi-tls").checked = s.wifiTLS !== false;
  $("aoa-enabled").checked = !!s.aoaEnabled;
  $("adb-tunnel-enabled").checked = !!s.adbTunnelEnabled;
  updateADBVisibility();
  $("usb-link-enabled").checked = !!s.usbLinkEnabled;
  $("usb-link-bind-address").value = s.usbLinkBindAddress || "0.0.0.0";
  $("usb-link-input-port").value = s.usbLinkInputPort || 37193;
  $("usb-link-video-port").value = s.usbLinkVideoPort || 37194;
  $("usb-link-max-bitrate").value = s.usbLinkMaxBitrate || "60M";
  const usbLinkAuthConfigured = !!s.usbLinkAuthTokenConfigured;
  $("usb-link-auth-token").value = "";
  $("usb-link-auth-token").placeholder = usbLinkAuthConfigured
    ? "configured (enter new token to rotate)"
    : "required when enabled";
  $("usb-link-tls").checked = s.usbLinkTLS !== false;
  $("require-adb-serial").checked = !!s.requirePreferredADB;
  $("local-cursor").checked = s.localCursor !== false;
  $("touch-enabled").checked = s.touchEnabled !== false;
  $("pen-enabled").checked = s.penEnabled !== false;
  $("keyboard-enabled").checked = s.keyboardEnabled !== false;
  $("keep-alive-on-background").checked = !!s.keepAliveOnBackground;
  $("video-scaling").value = s.videoScaling || "fill";
  $("auto-launch-android-app").checked = !!s.autoLaunchAndroidApp;
  $("zero-copy-capture").checked = !!s.zeroCopyCapture;
  $("temporal-layering").checked = !!s.temporalLayering;
  $("allow-tool-auto-install").checked = s.allowToolAutoInstall !== false;
  $("no-driver-install").checked = !!s.noDriverInstall;
}

async function refreshStatus() {
  try {
    const st = await api("/api/status");
    applyResolutionPolicy(st);
    applyPlatformVisibility(st.hostOS);
    applyFeatureVisibility(st.featureFlags || {});
    const v = st.video || {};
    const activeClient = st.activeClient || {};
    ensureNativeResolutionOption(activeClient.nativeWidth, activeClient.nativeHeight);
    const nativeW = Number(activeClient.nativeWidth) || 0;
    const nativeH = Number(activeClient.nativeHeight) || 0;
    const nativeLabel = nativeW > 0 && nativeH > 0 ? ` (${nativeW}x${nativeH})` : "";
    const activeClientName = [activeClient.platform, activeClient.type]
      .map((x) => String(x || "").trim())
      .filter((x) => x && x !== "unknown")
      .join(" ");
    const activeClientLabel = activeClientName ? `${activeClientName}${nativeLabel}` : nativeLabel.trim();
    const displayMode = String(v.displayMode || "").trim();
    const modeLabel = displayMode ? `, ${displayMode}` : "";
    $("dot").className = "dot" + (v.streaming ? " on" : "");
    $("status").textContent = v.streaming
      ? `Streaming (${v.encoder} @ ${v.bitrate}${modeLabel})${activeClientLabel ? ` — ${activeClientLabel}` : ""}`
      : "Idle — waiting for client";
    const warnings = Array.isArray(st.warnings) ? st.warnings : [];
    const setupPrompt = linuxSetupPrompt(warnings, st.hostOS);
    const lines = warnings.map((w) => `Warning: ${w}`);
    if (setupPrompt) lines.push(setupPrompt);
    $("warnings").hidden = warnings.length === 0;
    $("warnings").textContent = lines.join("\n");
    renderConnectionInfo(st);
    renderMetrics(st.metrics || {}, v, st.clientVideoStats || null);
    renderSetupChecklist(st.setup || {});
    const inputLockToggle = $("input-lock-toggle");
    if (!inputLockToggle.disabled) inputLockToggle.checked = !!st.inputLocked;
    autostartOn = !!st.autostart;
    $("autostart-state").textContent = st.autostartError
      ? `Unavailable: ${st.autostartError}`
      : (autostartOn ? "Enabled" : "Disabled");
    $("autostart-implementation").textContent =
      st.autostartImplementation || "Platform login service";
    const serviceButton = $("autostart-toggle");
    serviceButton.disabled = !!st.autostartError || serviceBusy;
    serviceButton.textContent = autostartOn ? "Disable service" : "Enable service";
    const activeDevice = String(st.activeInputDevice || "").trim();
    if (activeDevice !== currentCalibrationDevice) {
      currentCalibrationDevice = activeDevice;
      loadCalibration(activeDevice).catch((e) => showMsg(e.message, false));
    }
  } catch (e) {
    $("dot").className = "dot";
    $("status").textContent = "Host unreachable";
    $("warnings").hidden = true;
    $("connection-info").textContent = "";
    $("metrics-summary").textContent = "";
    $("setup-checklist").textContent = "Host unreachable";
  }
}

function setCalibrationForm(device, calibration) {
  $("calibration-device").value = device || "";
  $("calibration-scale-x").value = Number(calibration.scaleX ?? 1).toFixed(2);
  $("calibration-scale-y").value = Number(calibration.scaleY ?? 1).toFixed(2);
  $("calibration-offset-x").value = Number(calibration.offsetX ?? 0).toFixed(2);
  $("calibration-offset-y").value = Number(calibration.offsetY ?? 0).toFixed(2);
  const enabled = !!device;
  $("save-calibration-btn").disabled = !enabled;
  $("calibration-scale-x").disabled = !enabled;
  $("calibration-scale-y").disabled = !enabled;
  $("calibration-offset-x").disabled = !enabled;
  $("calibration-offset-y").disabled = !enabled;
}

async function loadCalibration(device) {
  if (!device) {
    setCalibrationForm("", { scaleX: 1, scaleY: 1, offsetX: 0, offsetY: 0 });
    return;
  }
  const body = await api(`/api/calibration?device=${encodeURIComponent(device)}`);
  setCalibrationForm(body.device || device, body.calibration || {});
}

function showMsg(text, ok) {
  const el = $("msg");
  el.textContent = text;
  el.className = ok ? "ok" : "err";
}

const AOA_STATE_LABELS = {
  searching: "looking for a tablet on USB",
  switching: "switching the tablet to accessory mode",
  "waiting-app": "tablet found, waiting for the DrawSplay app to open",
  linked: "linked",
  error: "error",
};

// Shows only the connection details for transports that are turned on.
function renderConnectionInfo(st) {
  const lines = [];
  if (st.aoaEnabled) {
    const aoa = st.aoa || {};
    const label = AOA_STATE_LABELS[aoa.state] || aoa.state || "unknown";
    lines.push(`USB accessory: ${label}${aoa.detail ? ` (${aoa.detail})` : ""}`);
  }
  if (st.adbTunnelEnabled) {
    const adb = st.adb || {};
    const serials = Array.isArray(adb.availableSerials) ? adb.availableSerials : [];
    const detail = adb.error
      ? `error: ${adb.error}`
      : (adb.ready ? `${serials.length} device(s) attached` : "not ready");
    lines.push(`ADB tunnel: ${detail}${adb.preferredSerial ? `, preferred ${adb.preferredSerial}` : ""}`);
  }
  if (!lines.length) lines.push("No USB transport is enabled. Turn on USB accessory or the ADB tunnel in settings.");
  $("connection-info").textContent = lines.join("\n");
}

function renderSetupChecklist(setup) {
  const el = $("setup-checklist");
  const items = Array.isArray(setup?.items) ? setup.items : [];
  el.replaceChildren();
  if (items.length === 0) {
    el.textContent = "No setup data yet.";
    return;
  }
  for (const item of items) {
    const row = document.createElement("section");
    row.className = `setup-item${item.ok ? " ready" : " needs-attention"}`;

    const heading = document.createElement("div");
    heading.className = "setup-item-heading";
    const indicator = document.createElement("span");
    indicator.className = "setup-item-indicator";
    indicator.setAttribute("aria-hidden", "true");
    indicator.textContent = item.ok ? "✓" : "!";
    const label = document.createElement("strong");
    label.textContent = item.label || "Setup check";
    const state = document.createElement("span");
    state.className = "setup-item-state";
    state.textContent = item.ok ? "Ready" : "Action needed";
    heading.append(indicator, label, state);
    row.appendChild(heading);

    const detail = document.createElement("div");
    detail.className = "setup-item-detail";
    detail.textContent = item.detail || "";
    row.appendChild(detail);

    if (!item.ok && item.action) {
      const instructions = document.createElement("div");
      instructions.className = "setup-item-action-text";
      instructions.textContent = item.action;
      row.appendChild(instructions);
    }

    if (!item.ok && item.command) {
      const command = document.createElement("code");
      command.className = "setup-item-command";
      command.textContent = item.command;
      const copy = document.createElement("button");
      copy.type = "button";
      copy.className = "setup-copy-button";
      copy.textContent = "Copy";
      copy.setAttribute("aria-label", `Copy instructions for ${item.label || "setup check"}`);
      copy.addEventListener("click", async () => {
        try {
          await navigator.clipboard.writeText(item.command);
          copy.textContent = "Copied";
          window.setTimeout(() => { copy.textContent = "Copy"; }, 1400);
        } catch (e) {
          showMsg(`Could not copy command: ${e.message}`, false);
        }
      });
      const commandRow = document.createElement("div");
      commandRow.className = "setup-item-command-row";
      commandRow.append(command, copy);
      row.appendChild(commandRow);
    }
    el.appendChild(row);
  }
}

function renderMetrics(metrics, video, clientVideoStats) {
  const el = $("metrics-summary");
  if (!metrics) {
    el.textContent = "";
    return;
  }
  const live =
    clientVideoStats && video?.streaming
      ? `, recv=${clientVideoStats.framesReceived || 0}, rend=${clientVideoStats.framesRendered || 0}, queue=${clientVideoStats.queuedFrames || 0}, decode=${Number(clientVideoStats.avgDecodeMs || 0).toFixed(1)}ms`
      : "";
  const line = `Health: video restarts=${metrics.videoRestarts || 0}, capture restarts=${metrics.captureRestarts || 0}, input restarts=${metrics.inputRestarts || 0}${video?.streaming ? `, live=${video.encoder} @ ${video.bitrate}` : ""}${live}`;
  el.textContent = line;
}

$("form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  $("save").disabled = true;
  try {
    let resolution = selectedResolutionDimensions();
    if (resolutionManagedByOS && loadedSettings) {
      const lockedWidth = Number.parseInt(loadedSettings.width, 10);
      const lockedHeight = Number.parseInt(loadedSettings.height, 10);
      if (Number.isFinite(lockedWidth) && Number.isFinite(lockedHeight) && lockedWidth > 0 && lockedHeight > 0) {
        resolution = { width: lockedWidth, height: lockedHeight };
      }
    }
    const body = {
      fps: parseInt($("fps").value, 10),
      width: resolution.width,
      height: resolution.height,
      ffmpeg: $("ffmpeg").value.trim(),
      adb: $("adb").value.trim(),
      preferredADBSerial: $("adb-serial").value.trim(),
      requirePreferredADB: $("require-adb-serial").checked,
      localCursor: $("local-cursor").checked,
      touchEnabled: $("touch-enabled").checked,
      penEnabled: $("pen-enabled").checked,
      keyboardEnabled: $("keyboard-enabled").checked,
      keepAliveOnBackground: $("keep-alive-on-background").checked,
      videoScaling: $("video-scaling").value,
      autoLaunchAndroidApp: $("auto-launch-android-app").checked,
      zeroCopyCapture: $("zero-copy-capture").checked,
      temporalLayering: $("temporal-layering").checked,
      allowToolAutoInstall: $("allow-tool-auto-install").checked,
      noDriverInstall: $("no-driver-install").checked,
      aoaEnabled: $("aoa-enabled").checked,
      adbTunnelEnabled: $("adb-tunnel-enabled").checked,
      usbLinkEnabled: $("usb-link-enabled").checked,
      usbLinkBindAddress: $("usb-link-bind-address").value.trim(),
      usbLinkInputPort: parseInt($("usb-link-input-port").value, 10),
      usbLinkVideoPort: parseInt($("usb-link-video-port").value, 10),
      usbLinkMaxBitrate: $("usb-link-max-bitrate").value.trim(),
      usbLinkTLS: $("usb-link-tls").checked,
    };
    const usbLinkAuthToken = $("usb-link-auth-token").value;
    if (usbLinkAuthToken.trim() !== "") {
      body.usbLinkAuthToken = usbLinkAuthToken;
    }
    if (featureFlags.wifi) {
      body.wifiEnabled = $("wifi-enabled").checked;
      body.wifiServiceName = $("wifi-service-name").value.trim();
      body.wifiBindAddress = $("wifi-bind-address").value.trim();
      body.wifiInputPort = parseInt($("wifi-input-port").value, 10);
      body.wifiVideoPort = parseInt($("wifi-video-port").value, 10);
      body.wifiMaxBitrate = $("wifi-max-bitrate").value.trim();
      const wifiAuthToken = $("wifi-auth-token").value;
      if (wifiAuthToken.trim() !== "") {
        body.wifiAuthToken = wifiAuthToken;
      }
      body.wifiTLS = $("wifi-tls").checked;
    }
    const res = await api("/api/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    loadedSettings = { ...(loadedSettings || {}), ...body };
    showMsg(res.restarted ? "Saved — stream restarting with new settings." : "Saved — applies on next connection.", true);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    $("save").disabled = false;
  }
});

$("input-lock-toggle").addEventListener("change", async (ev) => {
  const toggle = ev.target;
  const locked = !!toggle.checked;
  toggle.disabled = true;
  try {
    await api("/api/session/input-lock", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ locked }),
    });
    showMsg(locked ? "Tablet input locked." : "Tablet input unlocked.", true);
  } catch (e) {
    toggle.checked = !locked;
    showMsg(e.message, false);
  } finally {
    toggle.disabled = false;
  }
});

$("disconnect-session-btn").addEventListener("click", async () => {
  const btn = $("disconnect-session-btn");
  btn.disabled = true;
  try {
    await api("/api/session/disconnect", { method: "POST" });
    showMsg("Active client session disconnected. The app will auto-reconnect.", true);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    btn.disabled = false;
  }
});

$("pause-video-btn").addEventListener("click", async () => {
  const btn = $("pause-video-btn");
  btn.disabled = true;
  try {
    await api("/api/session/reconnect-video", { method: "POST" });
    showMsg("Video stream reconnect requested.", true);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    btn.disabled = false;
  }
});

function updateADBVisibility() {
  const on = $("adb-tunnel-enabled").checked;
  $("adb-options").hidden = !on;
  $("run-setup-remediation-btn").textContent = on
    ? "Restart ADB & re-check setup"
    : "Re-check setup";
}
$("adb-tunnel-enabled").addEventListener("change", updateADBVisibility);

$("run-setup-remediation-btn").addEventListener("click", async () => {
  const btn = $("run-setup-remediation-btn");
  btn.disabled = true;
  try {
    const result = await api("/api/setup/remediate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action: "restart-adb" }),
    });
    showMsg(
      result.videoRestarted
        ? "Setup repair checks ran; video restarted to use the X11 virtual display."
        : "Setup repair checks ran successfully.",
      true,
    );
    await refreshStatus();
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    btn.disabled = false;
  }
});

$("download-diagnostics-btn").addEventListener("click", async () => {
  const btn = $("download-diagnostics-btn");
  btn.disabled = true;
  try {
    const report = await api("/api/diagnostics");
    const blob = new Blob([JSON.stringify(report, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    const stamp = new Date().toISOString().replace(/[:.]/g, "-");
    a.href = url;
    a.download = `drawsplay-diagnostics-${stamp}.json`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    btn.disabled = false;
  }
});

$("resolution-preset").addEventListener("change", () => {
  setCustomResolutionVisibility();
  refreshFramerateOptions();
});

$("width").addEventListener("input", () => {
  if ($("resolution-preset").value === CUSTOM_RESOLUTION) {
    refreshFramerateOptions();
  }
});

$("height").addEventListener("input", () => {
  if ($("resolution-preset").value === CUSTOM_RESOLUTION) {
    refreshFramerateOptions();
  }
});

$("show-debug-console").addEventListener("change", (ev) => {
  const enabled = !!ev.target.checked;
  if (!enabled) resetDebugConsole();
  setDebugConsoleEnabled(enabled);
});

const themeToggle = $("theme-mode-toggle");
if (themeToggle) {
  themeToggle.addEventListener("change", (ev) => {
    const theme = ev.target.checked ? "light" : "dark";
    applyTheme(theme);
    try {
      localStorage.setItem(THEME_STORAGE_KEY, theme);
    } catch (_e) {
      // Ignore storage failures.
    }
  });
}

$("autostart-toggle").addEventListener("click", async () => {
  const want = !autostartOn;
  const button = $("autostart-toggle");
  serviceBusy = true;
  button.disabled = true;
  button.textContent = want ? "Enabling…" : "Disabling…";
  try {
    await api("/api/autostart", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: want }),
    });
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    serviceBusy = false;
    await refreshStatus();
  }
});

$("stop-btn").addEventListener("click", async () => {
  if (!confirm("Stop the DrawSplay host? The client will show \u201cWaiting for host\u201d until it is started again.")) return;
  const btn = $("stop-btn");
  btn.disabled = true;
  try {
    await api("/api/stop", { method: "POST" });
    $("dot").className = "dot";
    $("status").textContent = "Host stopped";
  } catch (e) {
    showMsg(e.message, false);
    btn.disabled = false;
  }
});

$("clear-mode-btn").addEventListener("click", async () => {
  if (!confirm("Clear the connected client's saved display resolution preference?")) return;
  const btn = $("clear-mode-btn");
  btn.disabled = true;
  try {
    await api("/api/tablet/clear-display-mode", { method: "POST" });
    showMsg("Tablet display preference cleared. It takes effect on next reconnect.", true);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    btn.disabled = false;
  }
});

const vddCustomResolutionSetupBtn = $("vdd-custom-resolution-setup-btn");
if (vddCustomResolutionSetupBtn) {
  vddCustomResolutionSetupBtn.addEventListener("click", async () => {
    const btn = vddCustomResolutionSetupBtn;
    btn.disabled = true;
    try {
      const res = await api("/api/vdd/custom-resolution/setup", { method: "POST" });
      showMsg(
        `VDD custom-resolution setup complete for ${res.width}x${res.height}. Retry your custom resolution.`,
        true
      );
    } catch (e) {
      showMsg(e.message, false);
    } finally {
      btn.disabled = false;
    }
  });
}

$("save-calibration-btn").addEventListener("click", async () => {
  const device = $("calibration-device").value.trim();
  if (!device) {
    showMsg("Connect a client first to save calibration.", false);
    return;
  }
  const btn = $("save-calibration-btn");
  btn.disabled = true;
  try {
    await api("/api/calibration", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        device,
        calibration: {
          scaleX: parseFloat($("calibration-scale-x").value),
          scaleY: parseFloat($("calibration-scale-y").value),
          offsetX: parseFloat($("calibration-offset-x").value),
          offsetY: parseFloat($("calibration-offset-y").value),
        },
      }),
    });
    showMsg("Calibration saved for connected client.", true);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    btn.disabled = false;
  }
});

ensureCSRF().catch((e) => showMsg(e.message, false));
try {
  initializeTheme();
} catch (_e) {
  // Keep UI functional even if theme initialization fails.
}
setCustomResolutionVisibility();
captureResolutionBaseLabels();
refreshFramerateOptions(FPS_DEFAULT);
$("show-debug-console").checked = false;
resetDebugConsole();
setDebugConsoleEnabled(false);
setCalibrationForm("", { scaleX: 1, scaleY: 1, offsetX: 0, offsetY: 0 });
loadSettings().catch((e) => showMsg(e.message, false));
refreshStatus();
setInterval(refreshStatus, 2000);
setInterval(refreshDebugConsole, 1000);
