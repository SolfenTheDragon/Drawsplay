"use strict";
const $ = (id) => document.getElementById(id);

// Links filled from server data are limited to https://github.com pages.
function safeHTTPSURL(value) {
  if (typeof value !== "string" || !value) return "";
  try {
    const u = new URL(value);
    if (u.protocol !== "https:" || u.hostname !== "github.com") return "";
    return u.href;
  } catch (_e) {
    return "";
  }
}
let autostartOn = false;
let serviceBusy = false;
const autostartToggles = document.querySelectorAll("[data-autostart-toggle]");
let selectedTheme = "system";
let csrfToken = "";
let csrfPromise = null;
let debugConsoleEnabled = false;
let debugLogNext = 0;
let debugLogLines = [];
let currentCalibrationDevice = "";
let loadedSettings = null;
let hostReachable = false;
let savingSettings = false;
let settingsDirty = false;
let savedCalibration = null;
let calibrationRequest = 0;
let calibrationLoadingDevice = "";
let hostStopped = false;
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
  if (nativeKey === activeNativeResolutionKey) return;

  if (activeNativeResolutionKey) {
    const prior = select.querySelector(`option[value="${activeNativeResolutionKey}"]`);
    if (prior && resolutionBaseLabels.has(activeNativeResolutionKey)) {
      prior.textContent = resolutionBaseLabels.get(activeNativeResolutionKey);
    }
    if (nativeResolutionWasCreated && prior) {
      if (prior.selected) {
        const [width, height] = activeNativeResolutionKey.split("x");
        $("width").value = width;
        $("height").value = height;
        select.value = CUSTOM_RESOLUTION;
        setCustomResolutionVisibility();
      }
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
  const custom = $("resolution-preset").value === CUSTOM_RESOLUTION;
  $("resolution-custom-fields").classList.toggle(
    "visible",
    custom
  );
  $("width").disabled = $("height").disabled = !custom || resolutionManagedByOS;
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
  if (![width, height].every(value => Number.isFinite(value) && value > 0)) return;
  const options = availableFrameratesForResolution(width, height);
  if (Number(preferredFPS) > 0 && !options.includes(Number(preferredFPS))) {
    options.push(Number(preferredFPS));
    options.sort((a, b) => a - b);
  }
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
  selectedTheme = theme;
  const isLight = theme === "light" || (theme === "system" && window.matchMedia("(prefers-color-scheme: light)").matches);
  document.documentElement.setAttribute("data-theme", isLight ? "light" : "dark");
  const name = theme[0].toUpperCase() + theme.slice(1);
  $("theme-mode-toggle").setAttribute("aria-label", `Theme: ${name}`);
  $("theme-mode-toggle").title = `${name} theme`;
  for (const icon of document.querySelectorAll("[data-theme-icon]")) icon.toggleAttribute("hidden", icon.dataset.themeIcon !== theme);
  for (const option of document.querySelectorAll("[data-theme-option]")) {
    option.setAttribute("aria-pressed", String(option.dataset.themeOption === theme));
  }
}

let hostOSName = "";

function applyPlatformVisibility(hostOS) {
  hostOSName = hostOS || "";
  const showWindowsOnly = hostOS === "windows";
  for (const el of document.querySelectorAll("[data-windows-only]")) {
    el.hidden = !showWindowsOnly;
  }
  for (const el of document.querySelectorAll("[data-linux-only]")) {
    el.hidden = hostOS !== "linux";
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
  show("wifi-pair-expiry", p.phase === "waiting");
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
    setText(text, "Enter this code in the tablet's Wi-Fi connect screen.");
    $("wifi-pair-expiry").textContent = `Code expires in ${left}s.`;
  } else if (p.phase === "approval") {
    setText(text, "Allow \"" + p.tablet + "\" (fingerprint " + p.fingerprint + ") to use this computer as a display and input device?");
  } else if (p.phase === "failed") {
    setText(text, p.error || "Pairing failed.");
  } else if (p.phase === "done") {
    setText(text, "Paired with \"" + p.tablet + "\".");
  } else if (v.paired) {
    setText(text, "Paired with \"" + (v.pairedName || "tablet") + "\".");
  } else {
    setText(text, "No tablet is paired yet.");
  }
}

function renderWifiInterfaces(v) {
  const select = $("paired-wifi-interface");
  const wanted = wifiSelectedInterface || v.interface || "";
  const names = (v.interfaces || []).slice();
  names.unshift("");
  if (wanted && !names.includes(wanted)) names.push(wanted);
  const signature = names.join("|");
  if (select.dataset.names !== signature) {
    select.dataset.names = signature;
    select.innerHTML = "";
    for (const name of names) {
      const opt = document.createElement("option");
      opt.value = name;
      opt.textContent = name || "Choose a network";
      select.appendChild(opt);
    }
  }
  if (document.activeElement !== select) select.value = wanted;
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
  setCustomResolutionVisibility();
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
  return `Linux setup required: ${hints.join("; ")}.`;
}

function initializeTheme() {
  let theme = "system";
  try {
    const stored = localStorage.getItem(THEME_STORAGE_KEY);
    if (["system", "light", "dark"].includes(stored)) {
      theme = stored;
    }
  } catch (_e) {
    // Follow the system when storage is unavailable.
  }
  applyTheme(theme);
  window.matchMedia("(prefers-color-scheme: light)").addEventListener("change", () => {
    if (selectedTheme === "system") applyTheme("system");
  });
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
  renderWifiInterfaces({ interface: wifiSelectedInterface, interfaces: [] });
  updateADBVisibility();
  updatePendingSettings();
}

function draftSettings() {
  const dimensions = selectedResolutionDimensions();
  const body = {
    ...readSettingControls(), width: dimensions.width, height: dimensions.height,
    fps: parseInt($("fps").value, 10),
  };
  if (resolutionManagedByOS || !resolutionDirty) {
    body.width = loadedSettings?.width;
    body.height = loadedSettings?.height;
  }
  if (featureFlags.wifi) {
    body.pairedWifiEnabled = $("paired-wifi-enabled").checked;
    body.pairedWifiInterface = $("paired-wifi-interface").value;
  }
  return body;
}

// settingEquals compares a form value with the saved one. The server omits
// settings at their zero value (an empty string, false, 0), while the form
// always reads a value of the control's type, so a missing saved value counts
// as the zero of the form value's type rather than as a change.
function settingEquals(draft, saved) {
  if (saved === undefined || saved === null) {
    if (typeof draft === "boolean") saved = false;
    else if (typeof draft === "number") saved = 0;
    else saved = "";
  }
  if (typeof draft === "number" || typeof saved === "number") {
    return Number(draft) === Number(saved) || (Number.isNaN(Number(draft)) && Number.isNaN(Number(saved)));
  }
  if (typeof draft === "boolean" || typeof saved === "boolean") return !!draft === !!saved;
  return String(draft) === String(saved);
}

function changedSettings() {
  if (!loadedSettings) return [];
  return Object.entries(draftSettings()).filter(([key, value]) => !settingEquals(value, loadedSettings[key])).map(([key]) => key);
}

// The save bar shows "Saved." for a moment after a save, then goes away
// until the next edit. Editing during that moment turns it back into the
// unsaved-changes bar at once.
let savedFlashTimer = 0;
const SAVED_FLASH_MS = 2500;

function flashSaved(message) {
  const bar = $("pending-changes");
  clearTimeout(savedFlashTimer);
  bar.classList.add("saved");
  $("saved-message").textContent = message;
  bar.hidden = false;
  savedFlashTimer = setTimeout(() => {
    savedFlashTimer = 0;
    bar.classList.remove("saved");
    bar.hidden = !settingsDirty;
  }, SAVED_FLASH_MS);
}

function updatePendingSettings() {
  settingsDirty = changedSettings().length > 0;
  const bar = $("pending-changes");
  if (savedFlashTimer && settingsDirty) {
    clearTimeout(savedFlashTimer);
    savedFlashTimer = 0;
    bar.classList.remove("saved");
  }
  if (!savedFlashTimer) bar.hidden = !settingsDirty;
  const available = hostReachable && !!loadedSettings && document.body.dataset.kind !== "pc";
  $("settings-fields").disabled = !available || savingSettings;
  $("save").disabled = !available || !settingsDirty || savingSettings;
  $("discard-settings-btn").disabled = savingSettings;
  $("paired-wifi-interface").required = featureFlags.wifi && $("paired-wifi-enabled").checked;
  const restartRequired = changedSettings().some(key => ["aoaEnabled", "adbTunnelEnabled", "noDriverInstall"].includes(key));
  $("change-impact").hidden = !restartRequired;
  $("change-impact").textContent = restartRequired ? "Requires a host restart." : "";
}

function setText(element, text) {
  if (element.textContent !== text) element.textContent = text;
}

function updateSectionNavigation() {
  const links = [...document.querySelectorAll(".section-nav a")];
  const current = links.find(link => !link.hidden && link.hash === location.hash) || links[0];
  for (const link of links) {
    if (link === current) link.setAttribute("aria-current", "location");
    else link.removeAttribute("aria-current");
  }
}

function revealNavigationTarget(hash) {
  const target = $(hash.slice(1));
  if (target instanceof HTMLDetailsElement) target.open = true;
}

document.querySelector(".section-nav").addEventListener("click", event => {
  const link = event.target.closest("a");
  if (link) revealNavigationTarget(link.hash);
});
window.addEventListener("hashchange", () => {
  updateSectionNavigation();
  revealNavigationTarget(location.hash);
});
updateSectionNavigation();
revealNavigationTarget(location.hash);

function updateLiveControls(connected) {
  for (const id of ["input-lock-toggle", "pause-video-btn", "disconnect-session-btn", "clear-mode-btn"]) {
    if (!$(id).dataset.busy) $(id).disabled = !connected;
  }
  for (const id of ["start-calibration-btn", "reset-calibration-btn", "cancel-calibration-btn", "save-calibration-btn"]) {
    if (!$(id).dataset.busy) $(id).disabled = !connected || !savedCalibration;
  }
}

function replaceWhenChanged(element, value, render) {
  const signature = JSON.stringify(value);
  if (element.dataset.signature === signature) return;
  element.dataset.signature = signature;
  element.replaceChildren();
  render();
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
  for (const target of ["connections", "troubleshooting"]) {
    document.querySelector(`.section-nav a[href="#${target}"]`).hidden = kind === "pc" || (target === "connections" && !featureFlags.wifi);
  }
  updateSectionNavigation();
  updatePendingSettings();

  // Badge and the small caption next to it.
  const badge = $("client-badge");
  badge.dataset.kind = kind;
  badge.textContent = {
    android: "Android display",
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
    setText($("status"), sessions.length > 1
      ? `Streaming to ${sessions.length} PC clients (${sessions.map((x) => x.client).join(", ")})`
      : `Streaming to PC "${session.client}" (${parts.join(", ")})`);
  } else if (kind === "none" && pc.running && !(st.video || {}).streaming) {
    setText($("status"), "Waiting for a tablet or a PC display");
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
  replaceWhenChanged(dl, sessions, () => {
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
  });
  $("pc-disconnect-btn").hidden = !session;
  const trusted = Array.isArray(pc.trusted) ? pc.trusted : [];
  $("pc-trusted-summary").textContent = `Trusted PC clients (${trusted.length})`;
  const list = $("pc-trusted-list");
  replaceWhenChanged(list, trusted, () => {
   for (const t of trusted) {
    const li = document.createElement("li");
    const code = document.createElement("code");
    code.textContent = t.key;
    li.append(code, document.createTextNode(` ${t.label || ""}`));
    list.appendChild(li);
   }
  });
}

async function refreshStatus() {
  try {
    const st = await api("/api/status");
    hostReachable = true;
    hostStopped = false;
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
      $("recommended-resolution-reason").textContent = `${why}Higher resolutions may stutter.`;
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
    $("dot").className = "dot" + (v.streaming ? " on" : "");
    if (st.connectionKind !== "pc") {
      setText($("status"), v.streaming
        ? `Display connected${displayMode ? ` (${displayMode})` : ""}`
        : "Waiting for a display");
    }
    const warnings = Array.isArray(st.warnings) ? st.warnings : [];
    const setupPrompt = linuxSetupPrompt(warnings, st.hostOS);
    const lines = warnings.map((w) => `Warning: ${w}`);
    if (setupPrompt) lines.push(setupPrompt);
    $("warnings").hidden = warnings.length === 0;
    setText($("warnings"), lines.join("\n"));
    const targetInfo = $("target-info");
    targetInfo.hidden = !activeClientLabel;
    targetInfo.textContent = activeClientLabel ? `Target Information: ${activeClientLabel}` : "";
    renderConnectionInfo(st);
    renderMetrics(st.metrics || {}, v, st.clientVideoStats || null);
    renderDesktopHost(st);
    renderSetupChecklist(st.setup || {});
    renderConnectionGuidance(st);
    const inputLockToggle = $("input-lock-toggle");
    if (!inputLockToggle.dataset.busy) inputLockToggle.checked = !!st.inputLocked;
    autostartOn = !!st.autostart;
    for (const toggle of autostartToggles) {
      if (!serviceBusy) toggle.checked = autostartOn;
      toggle.disabled = !!st.autostartError || serviceBusy;
      toggle.title = st.autostartError
        ? `Unavailable: ${st.autostartError}`
        : (st.autostartImplementation || "Platform login service");
    }
    const activeDevice = String(st.activeInputDevice || "").trim();
    if (activeDevice !== currentCalibrationDevice) {
      const discarded = calibrationDirty();
      if ($("calibration-dialog").open) $("calibration-dialog").close();
      currentCalibrationDevice = activeDevice;
      savedCalibration = null;
      setCalibrationForm(activeDevice, {});
      loadCalibration(activeDevice).catch((e) => showMsg(e.message, false, $("start-calibration-btn")));
      if (discarded) showMsg("The connected tablet changed. Its unsaved calibration was discarded.", false, $("start-calibration-btn"));
    } else if (activeDevice && !savedCalibration && calibrationLoadingDevice !== activeDevice) {
      loadCalibration(activeDevice).catch((e) => showMsg(e.message, false, $("start-calibration-btn")));
    }
    updateLiveControls(!!activeDevice);
  } catch (e) {
    hostReachable = false;
    $("dot").className = "dot";
    setText($("status"), hostStopped ? "Host stopped" : "Cannot reach DrawSplay");
    document.body.dataset.kind = "none";
    $("client-badge").dataset.kind = "none";
    $("client-badge").textContent = "No client";
    $("warnings").hidden = true;
    $("connection-info").textContent = "";
    $("metrics-summary").textContent = "";
    setText($("connection-guidance"), hostStopped
      ? "Start DrawSplay to reconnect."
      : "Check that DrawSplay is running. Retrying automatically.");
    $("connection-guidance").hidden = false;
    updatePendingSettings();
    updateLiveControls(false);
    for (const toggle of autostartToggles) toggle.disabled = true;
  }
}

function setCalibrationForm(device, calibration) {
  $("calibration-device").value = device || "";
  $("calibration-scale-x").value = Number(calibration.scaleX ?? 1).toFixed(4);
  $("calibration-scale-y").value = Number(calibration.scaleY ?? 1).toFixed(4);
  $("calibration-offset-x").value = Number(calibration.offsetX ?? 0).toFixed(4);
  $("calibration-offset-y").value = Number(calibration.offsetY ?? 0).toFixed(4);
  const enabled = !!device && !!savedCalibration;
  $("save-calibration-btn").disabled = !enabled;
  for (const id of ["start-calibration-btn", "reset-calibration-btn", "cancel-calibration-btn"]) $(id).disabled = !enabled;
  $("calibration-scale-x").disabled = !enabled;
  $("calibration-scale-y").disabled = !enabled;
  $("calibration-offset-x").disabled = !enabled;
  $("calibration-offset-y").disabled = !enabled;
}

async function loadCalibration(device) {
  const request = ++calibrationRequest;
  calibrationLoadingDevice = device;
  try {
    if (!device) {
      setCalibrationForm("", { scaleX: 1, scaleY: 1, offsetX: 0, offsetY: 0 });
      return;
    }
    const body = await api(`/api/calibration?device=${encodeURIComponent(device)}`);
    if (request !== calibrationRequest || device !== currentCalibrationDevice) return;
    savedCalibration = body.calibration || { scaleX: 1, scaleY: 1, offsetX: 0, offsetY: 0 };
    setCalibrationForm(body.device || device, savedCalibration);
  } finally {
    if (request === calibrationRequest) calibrationLoadingDevice = "";
  }
}

function showMsg(text, ok, origin) {
  const panel = (origin || feedbackSource)?.closest(".panel");
  let feedback = $("msg");
  if (panel) {
    feedback = panel.querySelector("#settings-feedback, :scope > .action-feedback");
    if (!feedback) {
      feedback = document.createElement("div");
      feedback.setAttribute("role", "status");
      panel.append(feedback);
    }
  }
  feedback.className = `action-feedback ${ok ? "ok" : "err"}`;
  feedback.setAttribute("role", ok ? "status" : "alert");
  setText(feedback, text);
}

let feedbackSource = null;
document.addEventListener("click", event => {
  const control = event.target.closest("button, input, select");
  if (control) feedbackSource = control;
}, true);

function renderConnectionGuidance(st) {
  const v = st.video || {};
  const state = st.aoa?.state;
  let text = "";
  if (v.streaming) {
    if (st.inputLocked) text = "Input locked. Unlock in Session controls.";
  } else if (state === "waiting-app") text = "Open DrawSplay and approve USB access on the tablet.";
  else if (state === "switching") text = "Keep the cable connected while USB mode changes.";
  else if (state === "error") text = "USB error. Check Troubleshooting.";
  setText($("connection-guidance"), text);
  $("connection-guidance").hidden = !text;
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
  const existing = new Map([...el.querySelectorAll(".setup-item")].map(row => [row.dataset.key, row]));
  if (items.length === 0) {
    if (el.contains(document.activeElement)) $("troubleshooting").querySelector("summary").focus();
    setText(el, "Setup checks are unavailable. Try Re-check setup.");
    return;
  }
  if (!existing.size) el.replaceChildren();
  for (const [index, item] of items.entries()) {
    const key = String(item.id || item.label || index);
    let row = existing.get(key);
    existing.delete(key);
    if (!row) {
      row = document.createElement("section");
      row.dataset.key = key;
      const heading = document.createElement("div");
      heading.className = "setup-item-heading";
      const indicator = document.createElement("span");
      indicator.className = "setup-item-indicator";
      indicator.setAttribute("aria-hidden", "true");
      const label = document.createElement("strong");
      const state = document.createElement("span");
      state.className = "setup-item-state";
      heading.append(indicator, label, state);
      const detail = document.createElement("div");
      detail.className = "setup-item-detail";
      const instructions = document.createElement("div");
      instructions.className = "setup-item-action-text";
      const command = document.createElement("code");
      command.className = "setup-item-command";
      const copy = document.createElement("button");
      copy.type = "button";
      copy.className = "setup-copy-button";
      copy.textContent = "Copy";
      copy.addEventListener("click", async () => {
        try {
          await navigator.clipboard.writeText(row.dataset.command);
          copy.textContent = "Copied";
          showMsg("Setup command copied.", true, copy);
          window.setTimeout(() => { copy.textContent = "Copy"; }, 1400);
        } catch (e) {
          showMsg(`Could not copy command: ${e.message}`, false, copy);
        }
      });
      const commandRow = document.createElement("div");
      commandRow.className = "setup-item-command-row";
      commandRow.append(command, copy);
      row.append(heading, detail, instructions, commandRow);
      el.appendChild(row);
    }
    row.className = `setup-item${item.ok ? " ready" : " needs-attention"}`;
    setText(row.querySelector(".setup-item-indicator"), item.ok ? "✓" : "!");
    setText(row.querySelector("strong"), item.label || "Setup check");
    setText(row.querySelector(".setup-item-state"), item.ok ? "Ready" : "Action needed");
    setText(row.querySelector(".setup-item-detail"), item.detail || "");
    setText(row.querySelector(".setup-item-action-text"), item.action || "");
    row.querySelector(".setup-item-action-text").hidden = item.ok || !item.action;
    row.dataset.command = item.command || "";
    setText(row.querySelector("code"), row.dataset.command);
    row.querySelector("button").setAttribute("aria-label", `Copy instructions for ${item.label || "setup check"}`);
    const commandRow = row.querySelector(".setup-item-command-row");
    const hideCommand = item.ok || !item.command;
    if (hideCommand && commandRow.contains(document.activeElement)) {
      $("troubleshooting").querySelector("summary").focus();
    }
    commandRow.hidden = hideCommand;
  }
  for (const row of existing.values()) {
    if (row.contains(document.activeElement)) $("troubleshooting").querySelector("summary").focus();
    row.remove();
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
  if (!hostReachable || document.body.dataset.kind === "pc" || savingSettings || !settingsDirty) return;
  savingSettings = true;
  updatePendingSettings();
  try {
    const changes = new Set(changedSettings());
    const body = Object.fromEntries(Object.entries(draftSettings()).filter(([key]) => changes.has(key)));
    const res = await api("/api/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    loadedSettings = res.settings || { ...(loadedSettings || {}), ...body };
    resolutionDirty = false;
    fillSettingControls(loadedSettings);
    syncResolutionPreset(loadedSettings.width, loadedSettings.height);
    refreshFramerateOptions(loadedSettings.fps);
    $("paired-wifi-enabled").checked = !!loadedSettings.pairedWifiEnabled;
    wifiSelectedInterface = loadedSettings.pairedWifiInterface || "";
    renderWifiInterfaces({ ...(wifiView || {}), interface: wifiSelectedInterface });
    updateADBVisibility();
    const message = res.restarted ? "Saved. Reconnecting video."
      : res.inputRestarted ? "Saved. Reconnecting input."
      : "Saved.";
    // Announced by the status region; nothing is focused, so the page does
    // not scroll away from the control the user just changed.
    showMsg(message, true, $("form"));
    flashSaved(message);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    savingSettings = false;
    updatePendingSettings();
  }
});

$("form").addEventListener("input", updatePendingSettings);
$("form").addEventListener("change", updatePendingSettings);
$("form").addEventListener("invalid", event => {
  const details = event.target.closest("details");
  if (details) details.open = true;
}, true);
$("discard-settings-btn").addEventListener("click", () => {
  if (!loadedSettings || savingSettings) return;
  fillSettingControls(loadedSettings);
  syncResolutionPreset(loadedSettings.width, loadedSettings.height);
  refreshFramerateOptions(loadedSettings.fps);
  $("paired-wifi-enabled").checked = !!loadedSettings.pairedWifiEnabled;
  wifiSelectedInterface = loadedSettings.pairedWifiInterface || "";
  $("paired-wifi-interface").value = wifiSelectedInterface;
  resolutionDirty = false;
  updateADBVisibility();
  updatePendingSettings();
  showMsg("Changes discarded.", true, $("form"));
});
window.addEventListener("beforeunload", event => {
  if (settingsDirty || calibrationDirty()) { event.preventDefault(); event.returnValue = ""; }
});

$("input-lock-toggle").addEventListener("change", async (ev) => {
  const toggle = ev.target;
  const locked = !!toggle.checked;
  toggle.dataset.busy = "true";
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
    delete toggle.dataset.busy;
    toggle.disabled = !hostReachable || !currentCalibrationDevice;
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
  btn.dataset.busy = "true";
  btn.disabled = true;
  try {
    await api("/api/session/disconnect", { method: "POST" });
    showMsg("Connection reset. Reconnecting.", true);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    delete btn.dataset.busy;
    btn.disabled = !hostReachable || !currentCalibrationDevice;
  }
});

$("pause-video-btn").addEventListener("click", async () => {
  const btn = $("pause-video-btn");
  btn.dataset.busy = "true";
  btn.disabled = true;
  try {
    await api("/api/session/reconnect-video", { method: "POST" });
    showMsg("Reconnecting video.", true);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    delete btn.dataset.busy;
    btn.disabled = !hostReachable || !currentCalibrationDevice;
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
  if (!$("adb-tunnel-enabled").checked && !$("aoa-enabled").checked) {
    $("aoa-enabled").checked = true;
    showMsg("USB accessory enabled; at least one USB transport is required.", true, $("adb-tunnel-enabled"));
  }
  updateADBVisibility();
});
$("aoa-enabled").addEventListener("change", () => {
  if (!$("aoa-enabled").checked) {
    $("adb-tunnel-enabled").checked = true;
    updateADBVisibility();
    showMsg("ADB enabled; at least one USB transport is required.", true, $("aoa-enabled"));
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
const themePicker = themeToggle.closest(".theme-picker");
const themeOptions = [...document.querySelectorAll("[data-theme-option]")];
let themeMenuPinned = false;

function setThemeMenuOpen(open) {
  $("theme-options").hidden = !open;
  themeToggle.setAttribute("aria-expanded", String(open));
  if (!open) themeMenuPinned = false;
}

themePicker.addEventListener("pointerenter", event => {
  if (event.pointerType === "mouse") setThemeMenuOpen(true);
});
themePicker.addEventListener("pointerleave", () => {
  if (!themeMenuPinned && !themePicker.contains(document.activeElement)) setThemeMenuOpen(false);
});
themePicker.addEventListener("focusout", event => {
  if (!themePicker.contains(event.relatedTarget)) setThemeMenuOpen(false);
});
themeToggle.addEventListener("click", () => {
  const open = !themeMenuPinned;
  setThemeMenuOpen(open);
  themeMenuPinned = open;
});
themePicker.addEventListener("keydown", event => {
  if (event.key === "Escape") {
    event.preventDefault();
    setThemeMenuOpen(false);
    themeToggle.focus();
  } else if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
    event.preventDefault();
    setThemeMenuOpen(true);
    let index = themeOptions.indexOf(document.activeElement);
    if (event.key === "Home") index = 0;
    else if (event.key === "End") index = themeOptions.length - 1;
    else if (index < 0) index = event.key === "ArrowDown" ? 0 : themeOptions.length - 1;
    else index = (index + (event.key === "ArrowDown" ? 1 : -1) + themeOptions.length) % themeOptions.length;
    themeOptions[index].focus();
  }
});
document.addEventListener("pointerdown", event => {
  if (!themePicker.contains(event.target)) setThemeMenuOpen(false);
});
for (const option of themeOptions) {
  option.addEventListener("click", () => {
    const theme = option.dataset.themeOption;
    applyTheme(theme);
    setThemeMenuOpen(false);
    themeToggle.focus();
    try {
      localStorage.setItem(THEME_STORAGE_KEY, theme);
    } catch (_e) {
      // Ignore storage failures.
    }
  });
}

for (const toggle of autostartToggles) {
  toggle.addEventListener("change", async () => {
    if (serviceBusy) return;
    const want = toggle.checked;
    serviceBusy = true;
    for (const control of autostartToggles) {
      control.checked = want;
      control.disabled = true;
    }
    try {
      await api("/api/autostart", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: want }),
      });
      autostartOn = want;
      showMsg(want ? "Startup enabled." : "Startup disabled.", true, toggle);
    } catch (e) {
      for (const control of autostartToggles) control.checked = autostartOn;
      showMsg(e.message, false, toggle);
    } finally {
      serviceBusy = false;
      await refreshStatus();
    }
  });
}

$("stop-btn").addEventListener("click", async () => {
  if (!confirm("Stop the DrawSplay host? The client will show \u201cWaiting for host\u201d until it is started again.")) return;
  const btn = $("stop-btn");
  btn.disabled = true;
  try {
    await api("/api/stop", { method: "POST" });
    hostStopped = true;
    hostReachable = false;
    updatePendingSettings();
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
  btn.dataset.busy = "true";
  btn.disabled = true;
  try {
    await api("/api/tablet/clear-display-mode", { method: "POST" });
    showMsg("Tablet display preference cleared. It takes effect on next reconnect.", true);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    delete btn.dataset.busy;
    btn.disabled = !hostReachable || !currentCalibrationDevice;
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
  btn.dataset.busy = "true";
  btn.disabled = true;
  try {
    const calibration = readCalibration();
    if (![calibration.scaleX, calibration.scaleY].every(value => Number.isFinite(value) && value >= 0.5 && value <= 1.5) ||
        ![calibration.offsetX, calibration.offsetY].every(value => Number.isFinite(value) && Math.abs(value) <= 0.25)) {
      throw new Error("Enter scales between 0.5 and 1.5, and offsets between -0.25 and 0.25.");
    }
    await api("/api/calibration", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        device,
        calibration,
      }),
    });
    if (device === currentCalibrationDevice) savedCalibration = calibration;
    showMsg("Calibration saved.", true, btn);
  } catch (e) {
    showMsg(e.message, false);
  } finally {
    delete btn.dataset.busy;
    btn.disabled = !hostReachable || !currentCalibrationDevice || !savedCalibration;
  }
});

function readCalibration() {
  return {
    scaleX: parseFloat($("calibration-scale-x").value),
    scaleY: parseFloat($("calibration-scale-y").value),
    offsetX: parseFloat($("calibration-offset-x").value),
    offsetY: parseFloat($("calibration-offset-y").value),
  };
}

function calibrationDirty() {
  return !!savedCalibration && Object.entries(readCalibration()).some(([key, value]) =>
    Math.abs(value - savedCalibration[key]) > 0.00006 || !Number.isFinite(value));
}

$("reset-calibration-btn").addEventListener("click", () => {
  setCalibrationForm(currentCalibrationDevice, { scaleX: 1, scaleY: 1, offsetX: 0, offsetY: 0 });
  showMsg("Defaults ready. Save calibration to apply.", true, $("reset-calibration-btn"));
});
$("cancel-calibration-btn").addEventListener("click", () => {
  if (savedCalibration) setCalibrationForm(currentCalibrationDevice, savedCalibration);
  showMsg("Calibration changes discarded.", true, $("cancel-calibration-btn"));
});

const calibrationTargets = [[0.2, 0.3], [0.8, 0.3], [0.8, 0.75], [0.2, 0.75]];
let calibrationSamples = [];
let calibrationCandidate = null;
let calibrationBasis = null;

function positionCalibrationTarget() {
  const [x, y] = calibrationTargets[calibrationSamples.length] || [0.5, 0.5];
  $("calibration-target").style.left = `${x * 100}%`;
  $("calibration-target").style.top = `${y * 100}%`;
  $("calibration-step").textContent = calibrationSamples.length < 4
    ? `Tap target ${calibrationSamples.length + 1} of 4 with your pen.`
    : "Move your pen. The dot previews the corrected position.";
}

$("start-calibration-btn").addEventListener("click", () => {
  if (!savedCalibration || !currentCalibrationDevice) return;
  calibrationSamples = [];
  calibrationCandidate = null;
  calibrationBasis = { ...savedCalibration };
  $("calibration-begin").hidden = false;
  $("calibration-begin").textContent = "Enter fullscreen & start";
  $("calibration-accept").hidden = true;
  $("calibration-area").hidden = true;
  $("calibration-preview").hidden = true;
  $("calibration-instructions").textContent = "Move this window to the tablet display. Tap four targets with your pen in fullscreen.";
  $("calibration-dialog").showModal();
});
$("calibration-begin").addEventListener("click", async () => {
  try {
    if (document.fullscreenElement !== $("calibration-workspace")) await $("calibration-workspace").requestFullscreen();
    $("calibration-area").hidden = false;
    $("calibration-begin").hidden = true;
    $("calibration-instructions").textContent = "Tap each target with your pen. Escape cancels.";
    positionCalibrationTarget();
  } catch (_e) {
    $("calibration-instructions").textContent = "Fullscreen unavailable. Use Manual adjustments or allow fullscreen and retry.";
  }
});
$("calibration-area").addEventListener("pointerup", event => {
  if (calibrationCandidate || calibrationSamples.length >= 4 || event.button !== 0 ||
      document.fullscreenElement !== $("calibration-workspace")) return;
  const [x, y] = calibrationTargets[calibrationSamples.length];
  calibrationSamples.push({
    targetX: x, targetY: y, observedX: event.clientX / innerWidth, observedY: event.clientY / innerHeight,
  });
  if (calibrationSamples.length === 4) {
    try {
      calibrationCandidate = solvePenCalibration(calibrationSamples, calibrationBasis);
      $("calibration-accept").hidden = false;
      $("calibration-preview").hidden = false;
    } catch (e) {
      $("calibration-instructions").textContent = e.message;
      $("calibration-area").hidden = true;
      $("calibration-begin").hidden = false;
      $("calibration-begin").textContent = "Try again";
      calibrationSamples = [];
      return;
    }
  }
  positionCalibrationTarget();
});
$("calibration-area").addEventListener("pointermove", event => {
  if (!calibrationCandidate) return;
  for (const [axis, coordinate, size, property] of [["X", event.clientX, innerWidth, "left"], ["Y", event.clientY, innerHeight, "top"]]) {
    const raw = (coordinate / size - calibrationBasis[`offset${axis}`]) / calibrationBasis[`scale${axis}`];
    const corrected = raw * calibrationCandidate[`scale${axis}`] + calibrationCandidate[`offset${axis}`];
    $("calibration-preview").style[property] = `${Math.max(0, Math.min(1, corrected)) * 100}%`;
  }
});
$("calibration-accept").addEventListener("click", () => {
  if (!calibrationCandidate) return;
  setCalibrationForm(currentCalibrationDevice, calibrationCandidate);
  $("calibration-dialog").close();
  showMsg("Calibration ready. Save calibration to apply.", true, $("start-calibration-btn"));
});
$("calibration-exit").addEventListener("click", () => $("calibration-dialog").close());
$("calibration-dialog").addEventListener("close", () => {
  if (document.fullscreenElement === $("calibration-workspace")) document.exitFullscreen().catch(() => {});
});
document.addEventListener("fullscreenchange", () => {
  if ($("calibration-dialog").open && !$("calibration-area").hidden &&
      document.fullscreenElement !== $("calibration-workspace")) $("calibration-dialog").close();
});

// ---- Updates (update.go) ----
const UPDATE_BUSY = new Set(["checking", "downloading", "installing", "restarting"]);
let updateView = null;
// Set while an install runs: the version that was running when it started,
// and whether the host has gone away since (it restarts into the new one).
let updateInstallFrom = "";
let updateHostWentAway = false;

function formatMiB(bytes) {
  return (Number(bytes || 0) / 1048576).toFixed(1) + " MB";
}

function updateSummaryText(v) {
  switch (v.state) {
    case "checking": return ["Checking for updates…", ""];
    case "current": return [`${v.current} is the latest version.`, "ok"];
    case "available": return [`DrawSplay ${v.latest} is available. You have ${v.current}.`, "available"];
    case "dev": return [`Development build ${v.current}. The latest release is ${v.latest}.`, ""];
    case "downloading": {
      const of = v.total ? ` of ${formatMiB(v.total)}` : "";
      return [`Downloading ${v.latest}… ${formatMiB(v.downloaded)}${of}`, "available"];
    }
    case "installing": return [`Installing ${v.latest}…`, "available"];
    case "restarting": return [`Restarting into ${v.latest}…`, "available"];
    case "failed": return [`You have ${v.current}.`, ""];
    default: return [v.autoCheck ? "Checking for updates…" : `You have ${v.current}. Not checked yet.`, ""];
  }
}

function renderUpdate(v) {
  updateView = v;
  const busy = UPDATE_BUSY.has(v.state);
  const [text, tone] = updateSummaryText(v);
  setText($("update-summary"), text);
  $("update-summary").setAttribute("aria-live", v.state === "downloading" ? "off" : "polite");
  $("update-summary").className = "update-summary" + (tone ? " " + tone : "");

  const progress = $("update-progress");
  progress.hidden = !(v.state === "downloading" || v.state === "installing" || v.state === "restarting");
  if (v.state === "downloading" && v.total > 0) {
    progress.value = Math.max(0, Math.min(100, (100 * v.downloaded) / v.total));
  } else progress.removeAttribute("value");

  const showNotes = !!v.notes && v.latest && v.state !== "current";
  $("update-notes-box").hidden = !showNotes;
  if (showNotes) {
    $("update-notes-version").textContent = v.latest;
    if ($("update-notes").textContent !== v.notes) $("update-notes").textContent = v.notes;
  }

  let note = v.state === "available" || v.state === "dev" ? (v.installNote || "") : "";
  if (v.state === "current" && v.checkedAt) {
    note = "Last checked " + new Date(v.checkedAt).toLocaleString() + ".";
  }
  if (v.state === "available" && v.canInstall) {
    note += (note ? " " : "") + "The tablet app offers its own update on its waiting screen.";
  }
  $("update-note").textContent = note;

  const result = $("update-result");
  const r = v.lastResult;
  replaceWhenChanged(result, r, () => {
   if (r) {
    const line = document.createElement("div");
    line.className = r.ok ? "update-summary ok" : "update-error";
    line.textContent = r.ok
      ? `Updated from ${r.from} to ${r.to}.`
      : `The update to ${r.to} did not finish, so ${v.current} is still running. If Windows asked for approval, it may have been declined.`;
    result.append(line);
    for (const w of r.warnings || []) {
      const el = document.createElement("div");
      el.className = "update-warning";
      el.textContent = w;
      result.append(el);
    }
   }
  });
  result.hidden = !r;

  $("update-error").hidden = !v.error;
  setText($("update-error"), v.error || "");

  const install = $("update-install-btn");
  install.hidden = !(v.canInstall && (v.state === "available" || busy));
  install.disabled = busy;
  install.textContent = v.latest ? `Install ${v.latest}` : "Install update";

  const link = $("update-release-link");
  const releaseURL = safeHTTPSURL(v.releaseURL);
  link.hidden = !releaseURL;
  if (releaseURL) link.href = releaseURL;
  else link.removeAttribute("href");
  link.textContent = v.state === "available" && !v.canInstall ? "Download from the release page" : "Release page";

  $("update-check-btn").disabled = busy;

  const footer = $("footer-update");
  footer.hidden = v.state !== "available";
  footer.textContent = v.latest ? `· ${v.latest} available` : "";
}

async function refreshUpdate() {
  let v;
  try {
    v = await api("/api/update");
  } catch (_e) {
    if (updateInstallFrom) {
      // The host is restarting into the new version.
      updateHostWentAway = true;
      $("update-summary").textContent = "Restarting DrawSplay… this page reloads when it is back.";
    }
    return;
  }
  if (updateInstallFrom && (updateHostWentAway || v.current !== updateInstallFrom)) {
    location.reload();
    return;
  }
  if (updateInstallFrom && !UPDATE_BUSY.has(v.state)) updateInstallFrom = ""; // the install failed
  renderUpdate(v);
}

// Runs every second like the other panels, but asks the host only every 15 s
// unless an update is under way.
let updateLastRefresh = 0;
let updateRefreshing = false;
async function tickUpdate() {
  const busy = updateInstallFrom || (updateView && UPDATE_BUSY.has(updateView.state));
  if (updateRefreshing || (!busy && Date.now() - updateLastRefresh < 15000)) return;
  updateRefreshing = true;
  updateLastRefresh = Date.now();
  try {
    await refreshUpdate();
  } finally {
    updateRefreshing = false;
  }
}

$("update-check-btn").addEventListener("click", async () => {
  $("update-check-btn").disabled = true;
  $("update-summary").textContent = "Checking for updates…";
  try {
    renderUpdate(await api("/api/update/check", { method: "POST" }));
  } catch (e) {
    $("update-error").hidden = false;
    $("update-error").textContent = e.message;
    $("update-check-btn").disabled = false;
  }
});

$("update-install-btn").addEventListener("click", async () => {
  const v = updateView;
  if (!v || !v.latest) return;
  const ok = confirm(
    `Install DrawSplay ${v.latest} now?\n\nDrawSplay restarts to finish the update, so a connected tablet disconnects for a few seconds.` +
    (hostOSName === "windows" ? " Windows asks for administrator approval." : ""));
  if (!ok) return;
  $("update-install-btn").disabled = true;
  try {
    updateInstallFrom = v.current;
    updateHostWentAway = false;
    renderUpdate(await api("/api/update/install", { method: "POST" }));
  } catch (e) {
    updateInstallFrom = "";
    $("update-error").hidden = false;
    $("update-error").textContent = e.message;
    $("update-install-btn").disabled = false;
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
tickUpdate();
setInterval(refreshStatus, 2000);
setInterval(tickUpdate, 1000);
setInterval(refreshWifiPairing, 2000);
setInterval(renderWifiPairing, 1000);
$("wifi-pair-start").addEventListener("click", () => wifiPairAction("start", { replace: !!(wifiView && wifiView.paired) }));
$("paired-wifi-interface").addEventListener("change", (e) => { wifiSelectedInterface = e.target.value; });
$("wifi-pair-cancel").addEventListener("click", () => wifiPairAction("cancel"));
$("wifi-pair-approve").addEventListener("click", () => wifiPairAction("approve"));
$("wifi-pair-deny").addEventListener("click", () => wifiPairAction("deny"));
setInterval(refreshDebugConsole, 1000);
