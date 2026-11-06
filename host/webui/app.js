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
// True once the user edits the resolution controls; otherwise Save resends the host's current resolution, which the OS display settings may have changed since the page loaded.
let resolutionDirty = false;
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

let wifiSelectedInterface = "";
let wifiView = null;

function formatPairCode(code) {
  return code && code.length === 8 ? code.slice(0, 4) + " " + code.slice(4) : code || "";
}

function renderWifiPairing() {
  const card = $("wifi-pairing-card");
  const v = wifiView;
  if (!featureFlags.wifi || !v || !v.enabled) {
    card.hidden = true;
    return;
  }
  card.hidden = false;
  const p = v.pairing || { phase: "idle" };
  const code = $("wifi-pair-code");
  const text = $("wifi-pair-text");
  const show = (id, on) => { $(id).hidden = !on; };
  code.hidden = p.phase !== "waiting" || !p.code;
  code.textContent = formatPairCode(p.code);
  show("wifi-pair-approve", p.phase === "approval");
  show("wifi-pair-deny", p.phase === "approval");
  show("wifi-pair-cancel", p.phase === "waiting");
  const idleLike = p.phase === "idle" || p.phase === "failed" || p.phase === "done";
  show("wifi-pair-start", idleLike);
  $("wifi-pair-start").textContent = v.paired ? "Pair another tablet" : "Show pairing code";
  if (p.phase === "waiting") {
    const left = Math.max(0, Math.round((p.expiresAtMs - Date.now()) / 1000));
    text.textContent = "On the tablet tap Wi-Fi connect, then enter this code. Expires in " + left + "s.";
  } else if (p.phase === "approval") {
    text.textContent = "Allow \"" + p.tablet + "\" (fingerprint " + p.fingerprint + ") to use this computer as a display and input device?";
  } else if (p.phase === "failed") {
    text.textContent = p.error || "Pairing failed.";
  } else if (p.phase === "done") {
    text.textContent = "Paired with \"" + p.tablet + "\". It will connect over Wi-Fi when USB is not plugged in.";
  } else if (v.paired) {
    text.textContent = "Paired with \"" + (v.pairedName || "tablet") + "\".";
  } else {
    text.textContent = "No tablet is paired yet.";
  }
}

function renderWifiInterfaces(v) {
  const select = $("paired-wifi-interface");
  const wanted = wifiSelectedInterface || v.interface || "";
  const names = (v.interfaces || []).slice();
  if (wanted && !names.includes(wanted)) names.push(wanted);
  const signature = names.join("|");
  if (select.dataset.names !== signature) {
    select.dataset.names = signature;
    select.innerHTML = "";
    for (const name of names) {
      const opt = document.createElement("option");
      opt.value = opt.textContent = name;
      select.appendChild(opt);
    }
  }
  if (document.activeElement !== select && wanted) select.value = wanted;
}

async function refreshWifiPairing() {
  if (!featureFlags.wifi) {
    renderWifiPairing();
    return;
  }
  try {
    wifiView = await api("/api/wifi/pairing");
    renderWifiInterfaces(wifiView);
  } catch (e) {
    wifiView = null;
  }
  renderWifiPairing();
}

async function wifiPairAction(action, body) {
  try {
    wifiView = await api("/api/wifi/pairing/" + action, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {}),
    });
  } catch (e) {
    $("wifi-pair-text").textContent = String(e.message || e);
    return;
  }
  renderWifiPairing();
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

// Form controls bound to a saved setting carry data-setting="<settings JSON
// key>"; these two functions fill and read all of them. The server always
// sends every setting with its default applied, so no defaults live here.
function settingControls() {
  return document.querySelectorAll("[data-setting]");
}

function fillSettingControls(s) {
  for (const el of settingControls()) {
    const value = s[el.dataset.setting];
    if (el.type === "checkbox") el.checked = !!value;
    else el.value = value ?? "";
  }
}

function readSettingControls() {
  const out = {};
  for (const el of settingControls()) {
    const key = el.dataset.setting;
    if (el.type === "checkbox") out[key] = el.checked;
    else if (el.type === "number") out[key] = Math.max(0, parseInt(el.value, 10) || 0);
    else out[key] = el.value.trim();
  }
  return out;
}

async function loadSettings() {
  const s = await api("/api/settings");
  loadedSettings = s;
  resolutionDirty = false;
  syncResolutionPreset(s.width, s.height);
  refreshFramerateOptions(s.fps);
  fillSettingControls(s);
  $("paired-wifi-enabled").checked = !!s.pairedWifiEnabled;
  wifiSelectedInterface = s.pairedWifiInterface || "";
  updateADBVisibility();
}

// ---- Connection kind: Android tablet (this host) or PC client (dd-host, read from its status file) ----

function durationText(sinceMs) {
  const s = Math.max(0, Math.floor((Date.now() - Number(sinceMs || 0)) / 1000));
  if (!Number.isFinite(s) || !sinceMs) return "";
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : (m > 0 ? `${m}m ${s % 60}s` : `${s}s`);
}

const PC_BACKEND_LABELS = {
  "x11-virtual": "virtual X11 monitor",
  "windows-virtual": "virtual Windows monitor",
  "wayland-virtual": "virtual Wayland monitor",
  "wayland-portal": "Wayland desktop portal",
  "wayland-mirror": "Wayland screen mirror",
  mirror: "screen mirror",
  test: "test pattern",
};

function renderDesktopHost(st) {
  const kind = ["android", "pc", "both"].includes(st.connectionKind) ? st.connectionKind : "none";
  const pc = st.desktopHost || {};
  const session = pc.running && pc.session ? pc.session : null;
  const sessions = pc.running && Array.isArray(pc.sessions) && pc.sessions.length ? pc.sessions : (session ? [session] : []);
  document.body.dataset.kind = kind;

  // Badge and the small caption next to it.
  const activeClient = st.activeClient || {};
  const androidName = [activeClient.platform, activeClient.type]
    .map((x) => String(x || "").trim()).filter((x) => x && x !== "unknown").join(" ");
  const badge = $("client-badge");
  badge.dataset.kind = kind;
  badge.textContent = {
    android: `Android${androidName ? ` · ${androidName}` : ""}`,
    pc: `PC${session ? ` · ${session.client}${sessions.length > 1 ? ` +${sessions.length - 1}` : ""}` : ""}`,
    both: "Android + PC",
    none: "No client",
  }[kind];
  const hosts = ["Android host"];
  if (pc.running) hosts.push("PC host");
  $("status-meta").textContent = kind === "pc"
    ? "PC host session"
    : (pc.running ? `${hosts.join(" + ")} running` : "USB / LAN stream monitor");

  // Status line: the existing renderer describes the Android pipeline only.
  if (kind === "pc" && session) {
    const parts = [`${session.width}x${session.height} @ ${session.fps} fps`];
    if (session.encoder) parts.push(session.encoder);
    $("dot").className = "dot on";
    $("status").textContent = sessions.length > 1
      ? `Streaming to ${sessions.length} PC clients (${sessions.map((x) => x.client).join(", ")})`
      : `Streaming to PC "${session.client}" (${parts.join(", ")})`;
  } else if (kind === "none" && pc.running && !(st.video || {}).streaming) {
    $("status").textContent = "Idle — waiting for a tablet or a PC client";
  }

  // PC panel: shown whenever dd-host is running, with the live session when there is one.
  const panel = $("pc-panel");
  panel.hidden = !pc.running;
  $("pc-settings-note").hidden = kind !== "pc";
  if (!pc.running) return;
  $("pc-summary").textContent = session
    ? (sessions.length > 1
      ? `${sessions.length} PC clients connected.`
      : `Connected for ${durationText(session.sinceUnixMs) || "a moment"}.`)
    : `Waiting for a PC client on ${pc.listen || "the default port"} (dd-host ${pc.version || ""}, ${pc.encoder || "encoder unknown"}).`;
  const dl = $("pc-session");
  dl.hidden = !session;
  dl.replaceChildren();
  if (session) {
    const rows = [];
    sessions.forEach((x, i) => {
      const n = sessions.length > 1 ? ` ${i + 1}` : "";
      rows.push(
        [`Client${n}`, x.client],
        [`Address${n}`, x.peer],
        [`Stream${n}`, `${x.width}x${x.height} @ ${x.fps} fps`],
        [`Encoder${n}`, x.encoder],
        [`Screen${n}`, `${PC_BACKEND_LABELS[x.backend] || x.backend}${x.virtualDisplay ? " (extends your desktop)" : ""}`],
      );
    });
    for (const [k, v] of rows) {
      const dt = document.createElement("dt");
      dt.textContent = k;
      const dd = document.createElement("dd");
      dd.textContent = v || "—";
      dl.append(dt, dd);
    }
  }
  $("pc-disconnect-btn").hidden = !session;
  const trusted = Array.isArray(pc.trusted) ? pc.trusted : [];
  $("pc-trusted-summary").textContent = `Trusted PC clients (${trusted.length})`;
  const list = $("pc-trusted-list");
  list.replaceChildren();
  for (const t of trusted) {
    const li = document.createElement("li");
    const code = document.createElement("code");
    code.textContent = t.key;
    li.append(code, document.createTextNode(` ${t.label || ""}`));
    list.appendChild(li);
  }
}

async function refreshStatus() {
  try {
    const st = await api("/api/status");
    applyResolutionPolicy(st);
    const versionEl = document.getElementById("host-version");
    if (versionEl && st.version) {
      versionEl.textContent = st.version;
      if (/^v\d+\.\d+\.\d+$/.test(st.version)) {
        versionEl.href = "https://github.com/SolfenTheDragon/Drawsplay/releases/tag/" + st.version;
      } else {
        versionEl.removeAttribute("href");
      }
    }
    applyPlatformVisibility(st.hostOS);
    applyFeatureVisibility(st.featureFlags || {});
    const v = st.video || {};
    const activeClient = st.activeClient || {};
    ensureNativeResolutionOption(activeClient.nativeWidth, activeClient.nativeHeight);
    const nativeW = Number(activeClient.nativeWidth) || 0;
    const nativeH = Number(activeClient.nativeHeight) || 0;
    const recHint = $("recommended-resolution-hint");
    const recW = Number(activeClient.recommendedWidth) || 0;
    const recH = Number(activeClient.recommendedHeight) || 0;
    if (recW > 0 && recH > 0) {
      $("recommended-resolution-value").textContent = `${recW}x${recH}`;
      const why = activeClient.recommendedReason ? `Because of ${activeClient.recommendedReason}. ` : "";
      $("recommended-resolution-reason").textContent = `${why}Higher resolutions may cause lag or freezes on this device.`;
      recHint.hidden = false;
    } else {
      recHint.hidden = true;
    }
    const refreshRates = Array.isArray(activeClient.refreshRates) ? activeClient.refreshRates : [];
    const maxHz = refreshRates.length > 0 ? Math.max(...refreshRates) : 0;
    const nativeParts = [];
    if (nativeW > 0 && nativeH > 0) nativeParts.push(`${nativeW}x${nativeH}`);
    if (maxHz > 0) nativeParts.push(`${refreshRates.join("/")} Hz`);
    const nativeLabel = nativeParts.length > 0 ? ` (${nativeParts.join(", ")})` : "";
    const activeClientName = [activeClient.platform, activeClient.type]
      .map((x) => String(x || "").trim())
      .filter((x) => x && x !== "unknown")
      .join(" ");
    const activeClientLabel = activeClientName ? `${activeClientName}${nativeLabel}` : nativeLabel.trim();
    const displayMode = String(v.displayMode || "").trim();
    const modeLabel = displayMode ? `, ${displayMode}` : "";
    $("dot").className = "dot" + (v.streaming ? " on" : "");
    $("status").textContent = v.streaming
      ? `Streaming (${v.encoder} @ ${v.bitrate}${modeLabel})`
      : "Idle — waiting for client";
    const warnings = Array.isArray(st.warnings) ? st.warnings : [];
    const setupPrompt = linuxSetupPrompt(warnings, st.hostOS);
    const lines = warnings.map((w) => `Warning: ${w}`);
    if (setupPrompt) lines.push(setupPrompt);
    $("warnings").hidden = warnings.length === 0;
    $("warnings").textContent = lines.join("\n");
    const targetInfo = $("target-info");
    targetInfo.hidden = !activeClientLabel;
    targetInfo.textContent = activeClientLabel ? `Target Information: ${activeClientLabel}` : "";
    renderConnectionInfo(st);
    renderMetrics(st.metrics || {}, v, st.clientVideoStats || null);
    renderDesktopHost(st);
    renderSetupChecklist(st.setup || {});
    const inputLockToggle = $("input-lock-toggle");
    if (!inputLockToggle.disabled) inputLockToggle.checked = !!st.inputLocked;
    autostartOn = !!st.autostart;
    const serviceToggle = $("autostart-toggle");
    serviceToggle.checked = autostartOn;
    serviceToggle.disabled = !!st.autostartError || serviceBusy;
    serviceToggle.title = st.autostartError
      ? `Unavailable: ${st.autostartError}`
      : (st.autostartImplementation || "Platform login service");
    const activeDevice = String(st.activeInputDevice || "").trim();
    if (activeDevice !== currentCalibrationDevice) {
      currentCalibrationDevice = activeDevice;
      loadCalibration(activeDevice).catch((e) => showMsg(e.message, false));
    }
  } catch (e) {
    $("dot").className = "dot";
    $("status").textContent = "Host unreachable";
    document.body.dataset.kind = "none";
    $("client-badge").dataset.kind = "none";
    $("client-badge").textContent = "No client";
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
  "file-transfer": "tablet is in File transfer mode",
  "manual-switch": "automatic switching is off",
};

// Shows only the connection details for transports that are turned on.
// Tablet-reported RSSI thresholds, in dBm.
function wifiSignalQuality(rssi) {
  if (!Number.isFinite(rssi) || rssi >= 0) return null;
  if (rssi >= -55) return { bars: 4, label: "Excellent", tone: "ok" };
  if (rssi >= -67) return { bars: 3, label: "Good", tone: "ok" };
  if (rssi >= -75) return { bars: 2, label: "Fair", tone: "warn" };
  return { bars: 1, label: "Weak", tone: "err" };
}

function renderWifiSignal(stats) {
  const row = document.createElement("div");
  row.className = "wifi-signal";
  const rssi = stats ? stats.wifiRssiDbm : undefined;
  const quality = wifiSignalQuality(rssi);
  const meter = document.createElement("span");
  meter.className = `signal-bars${quality ? ` ${quality.tone}` : ""}`;
  meter.setAttribute("aria-hidden", "true");
  for (let i = 1; i <= 4; i++) {
    const bar = document.createElement("span");
    if (quality && i <= quality.bars) bar.className = "lit";
    meter.appendChild(bar);
  }
  const text = document.createElement("span");
  if (!quality) {
    text.textContent = "Signal: waiting for tablet…";
  } else {
    const parts = [`Signal: ${quality.label} (${rssi} dBm)`];
    if (stats.wifiLinkMbps > 0) parts.push(`${stats.wifiLinkMbps} Mbps`);
    if (stats.wifiFrequencyMhz > 0) parts.push(stats.wifiFrequencyMhz >= 5900 ? "6 GHz" : stats.wifiFrequencyMhz >= 4900 ? "5 GHz" : "2.4 GHz");
    text.textContent = parts.join(" · ");
  }
  row.append(meter, text);
  return row;
}

function renderConnectionInfo(st) {
  if (st.activeTransport === "wifi-paired") {
    // A live Wi-Fi session makes the USB details irrelevant.
    $("switch-aoa-btn").hidden = true;
    const name = (wifiView && wifiView.pairedName) || "tablet";
    const info = $("connection-info");
    info.textContent = `Wi-Fi: connected to ${name}`;
    info.appendChild(renderWifiSignal(st.clientVideoStats));
    return;
  }
  const lines = [];
  if (st.aoaEnabled) {
    const aoa = st.aoa || {};
    const label = AOA_STATE_LABELS[aoa.state] || aoa.state || "unknown";
    lines.push(`USB accessory: ${label}${aoa.detail ? ` (${aoa.detail})` : ""}`);
  }
  const aoaState = (st.aoa || {}).state;
  $("switch-aoa-btn").hidden = !st.aoaEnabled || ["linked", "waiting-app", "switching"].includes(aoaState);
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
    if ((resolutionManagedByOS || !resolutionDirty) && loadedSettings) {
      const current = await api("/api/settings");
      const lockedWidth = Number.parseInt(current.width, 10);
      const lockedHeight = Number.parseInt(current.height, 10);
      if (Number.isFinite(lockedWidth) && Number.isFinite(lockedHeight) && lockedWidth > 0 && lockedHeight > 0) {
        resolution = { width: lockedWidth, height: lockedHeight };
      }
    }
    const body = {
      fps: parseInt($("fps").value, 10),
      width: resolution.width,
      height: resolution.height,
      ...readSettingControls(),
    };
    if (featureFlags.wifi) {
      body.pairedWifiEnabled = $("paired-wifi-enabled").checked;
      body.pairedWifiInterface = $("paired-wifi-interface").value;
    }
    const res = await api("/api/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    loadedSettings = { ...(loadedSettings || {}), ...body };
    resolutionDirty = false;
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

$("pc-disconnect-btn").addEventListener("click", async () => {
  const btn = $("pc-disconnect-btn");
  btn.disabled = true;
  try {
    await api("/api/desktop/disconnect", { method: "POST" });
    showMsg("PC client disconnect requested.", true);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    btn.disabled = false;
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
$("adb-tunnel-enabled").addEventListener("change", () => {
  if (!$("adb-tunnel-enabled").checked) $("aoa-enabled").checked = true;
  updateADBVisibility();
});
$("aoa-enabled").addEventListener("change", () => {
  if (!$("aoa-enabled").checked) {
    $("adb-tunnel-enabled").checked = true;
    updateADBVisibility();
  }
});

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
  resolutionDirty = true;
  setCustomResolutionVisibility();
  refreshFramerateOptions();
});

$("width").addEventListener("input", () => {
  resolutionDirty = true;
  if ($("resolution-preset").value === CUSTOM_RESOLUTION) {
    refreshFramerateOptions();
  }
});

$("height").addEventListener("input", () => {
  resolutionDirty = true;
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

$("autostart-toggle").addEventListener("change", async () => {
  const want = $("autostart-toggle").checked;
  const button = $("autostart-toggle");
  serviceBusy = true;
  button.disabled = true;
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

$("switch-aoa-btn").addEventListener("click", async () => {
  const btn = $("switch-aoa-btn");
  btn.disabled = true;
  try {
    await api("/api/tablet/switch-aoa", { method: "POST" });
    showMsg("Switching the tablet to USB accessory mode. Accept the prompt on the tablet.", true);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    btn.disabled = false;
  }
});

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
if (window.EventSource) {
  // The tray asks an already-open settings page to come to the front.
  new EventSource("/api/focus-events").addEventListener("focus", () => {
    window.focus();
    const base = document.title.replace(/^● /, "");
    document.title = "● " + base;
    const clear = () => { document.title = base; window.removeEventListener("focus", clear); };
    window.addEventListener("focus", clear);
  });
}
setInterval(refreshStatus, 2000);
setInterval(refreshWifiPairing, 2000);
setInterval(renderWifiPairing, 1000);
$("wifi-pair-start").addEventListener("click", () => wifiPairAction("start", { replace: !!(wifiView && wifiView.paired) }));
$("paired-wifi-interface").addEventListener("change", (e) => { wifiSelectedInterface = e.target.value; });
$("wifi-pair-cancel").addEventListener("click", () => wifiPairAction("cancel"));
$("wifi-pair-approve").addEventListener("click", () => wifiPairAction("approve"));
$("wifi-pair-deny").addEventListener("click", () => wifiPairAction("deny"));
setInterval(refreshDebugConsole, 1000);
