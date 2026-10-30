// focal studio — browser client for `focal serve`. Plain JS, no build step.
"use strict";

const $ = (id) => document.getElementById(id);
const state = {
  connected: false,
  connecting: false,
  status: null,
  tl: null,
  tlSetupView: false,
  media: [],
  seenMedia: new Set(),
  evDragging: false,
  capturing: false,
};

// ── API ──────────────────────────────────────────────────────────────────────

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) {
    const err = new Error((data && data.error) || res.statusText);
    err.status = res.status;
    throw err;
  }
  return data;
}

// ── Toasts & formatting ──────────────────────────────────────────────────────

function toast(msg, kind = "") {
  const el = document.createElement("div");
  el.className = "toast " + kind;
  el.textContent = msg;
  $("toasts").appendChild(el);
  setTimeout(() => el.remove(), kind === "err" ? 6000 : 3500);
}

function fmtDur(sec) {
  if (!isFinite(sec)) return "∞";
  sec = Math.max(0, Math.round(sec));
  if (sec < 60) return sec + "s";
  if (sec < 3600) return Math.floor(sec / 60) + "m " + String(sec % 60).padStart(2, "0") + "s";
  return Math.floor(sec / 3600) + "h " + String(Math.floor((sec % 3600) / 60)).padStart(2, "0") + "m";
}

function fmtEV(index, step) {
  const ev = index * step;
  if (Math.abs(ev) < 0.01) return "±0.0";
  return (ev > 0 ? "+" : "−") + Math.abs(ev).toFixed(1);
}

function fmtBytes(n) {
  if (n < 1024 * 1024) return (n / 1024).toFixed(0) + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

const has = (api) => !!(state.status && state.status.availableApis && state.status.availableApis.includes(api));

// ── Connection ───────────────────────────────────────────────────────────────

function setConnState(kind, label) {
  const pill = $("pillConn");
  pill.querySelector(".dot").className = "dot " + kind;
  pill.querySelector("strong").textContent = label;
}

async function connect(explicit) {
  if (state.connecting) return;
  state.connecting = true;
  setConnState("busy", "Searching…");
  $("btnConnect").disabled = true;
  $("btnConnect").innerHTML = '<span class="spinner"></span> Searching for camera…';
  try {
    const info = await api("POST", "/api/camera/connect");
    onConnected(info);
    if (explicit) toast("Connected to " + info.device.friendlyName, "ok");
  } catch (e) {
    onDisconnected(e.message);
  } finally {
    state.connecting = false;
    $("btnConnect").disabled = false;
    $("btnConnect").textContent = "Connect";
  }
}

function onConnected(info) {
  const wasConnected = state.connected;
  state.connected = true;
  setConnState("ok", info && info.device ? info.device.friendlyName : "Connected");
  $("connectCard").hidden = true;
  $("connectErr").hidden = true;
  if (!wasConnected) {
    startLiveview();
    pollStatus();
  }
}

function onDisconnected(message) {
  state.connected = false;
  state.status = null;
  setConnState("bad", "Not connected");
  stopLiveview();
  $("connectCard").hidden = false;
  $("vfLoading").hidden = true;
  if (message) {
    $("connectErr").textContent = message;
    $("connectErr").hidden = false;
  }
  renderStatus();
}

// ── Live view ────────────────────────────────────────────────────────────────

let liveRetry = null;

function startLiveview() {
  clearTimeout(liveRetry);
  const img = $("live");
  $("vfLoading").hidden = false;
  $("vfLoadingText").textContent = "Starting live view…";
  img.src = "/api/liveview?t=" + Date.now();
}

function stopLiveview() {
  clearTimeout(liveRetry);
  $("live").removeAttribute("src");
  $("vf").classList.remove("streaming");
}

$("live").addEventListener("load", () => {
  $("vf").classList.add("streaming");
  $("vfLoading").hidden = true;
});
$("live").addEventListener("error", () => {
  $("vf").classList.remove("streaming");
  if (!state.connected) return;
  $("vfLoadingText").textContent = "Reconnecting live view…";
  liveRetry = setTimeout(startLiveview, 2500);
});

// ── Status polling & rendering ───────────────────────────────────────────────

let statusTimer = null;

async function pollStatus() {
  clearTimeout(statusTimer);
  if (!state.connected) return;
  if (!document.hidden) {
    try {
      state.status = await api("GET", "/api/status");
      renderStatus();
    } catch (e) {
      if (e.status === 503 || e.status === 502) {
        onDisconnected(e.message);
        return;
      }
    }
  }
  statusTimer = setTimeout(pollStatus, 1500);
}

const MODE_ABBR = {
  "Program Auto": "P", "Aperture": "A", "Shutter": "S", "Manual": "M",
  "Intelligent Auto": "iAUTO", "Superior Auto": "iAUTO+",
};

const DIALS = {
  dialsExposure: [
    { key: "exposureMode", label: "Mode", setter: "setExposureMode" },
    { key: "shutterSpeed", label: "Shutter", setter: "setShutterSpeed" },
    { key: "fNumber", label: "Aperture", setter: "setFNumber", fmt: (v) => "f/" + v },
    { key: "isoSpeedRate", label: "ISO", setter: "setIsoSpeedRate" },
  ],
  dialsImage: [
    { key: "whiteBalance", label: "White balance", setter: "setWhiteBalance" },
    { key: "focusMode", label: "Focus", setter: "setFocusMode" },
    { key: "postviewImageSize", label: "Transfer size", setter: "setPostviewImageSize", fmt: (v) => (v === "Original" ? "Full res" : v) },
    { key: "selfTimer", label: "Self-timer", setter: "setSelfTimer", fmt: (v) => (v === "0" ? "Off" : v + "s") },
    { key: "flashMode", label: "Flash", setter: "setFlashMode" },
  ],
};

function buildDials() {
  for (const [container, dials] of Object.entries(DIALS)) {
    const root = $(container);
    for (const d of dials) {
      const row = document.createElement("div");
      row.className = "dial na";
      row.innerHTML = `<label>${d.label}</label><button class="step" title="Previous">‹</button><select disabled><option>—</option></select><button class="step" title="Next">›</button>`;
      const [prev, sel, next] = [row.children[1], row.children[2], row.children[3]];
      d.row = row; d.sel = sel; d.prev = prev; d.next = next; d.options = "";
      sel.addEventListener("change", () => applySetting(d, sel.value));
      prev.addEventListener("click", () => stepSetting(d, -1));
      next.addEventListener("click", () => stepSetting(d, +1));
      root.appendChild(row);
    }
  }
}

function stepSetting(d, dir) {
  const opts = [...d.sel.options].map((o) => o.value);
  const i = opts.indexOf(d.sel.value) + dir;
  if (i >= 0 && i < opts.length) {
    d.sel.value = opts[i];
    applySetting(d, opts[i]);
  }
}

async function applySetting(d, value) {
  d.pending = value;
  try {
    await api("PUT", "/api/settings/" + d.key, { value });
  } catch (e) {
    toast(d.label + ": " + e.message, "err");
  } finally {
    d.pending = undefined;
    pollStatus();
  }
}

function renderDial(d) {
  const st = state.status && state.status[d.key];
  const enabled = !!st && has(d.setter);
  d.row.classList.toggle("na", !st);
  if (!st) {
    d.sel.innerHTML = "<option>—</option>";
    d.options = "";
  } else {
    const cands = st.candidates && st.candidates.length ? st.candidates : [st.current];
    const sig = cands.join("|");
    if (sig !== d.options && document.activeElement !== d.sel) {
      d.sel.innerHTML = "";
      for (const c of cands) {
        const o = document.createElement("option");
        o.value = c;
        o.textContent = d.fmt ? d.fmt(c) : c;
        d.sel.appendChild(o);
      }
      d.options = sig;
    }
    if (d.pending === undefined && document.activeElement !== d.sel) d.sel.value = st.current;
  }
  const multi = d.sel.options.length > 1;
  d.sel.disabled = !enabled || !multi;
  const idx = d.sel.selectedIndex;
  d.prev.disabled = d.sel.disabled || idx <= 0;
  d.next.disabled = d.sel.disabled || idx >= d.sel.options.length - 1;
}

function renderStatus() {
  const st = state.status;
  const vf = $("vf");
  vf.classList.toggle("ready", !!st);
  for (const dials of Object.values(DIALS)) dials.forEach(renderDial);

  // Exposure compensation slider.
  const ev = st && st.exposureCompensation;
  const slider = $("ev");
  $("evDial").classList.toggle("na", !ev);
  slider.disabled = !ev || !has("setExposureCompensation");
  if (ev && !state.evDragging) {
    slider.min = ev.min; slider.max = ev.max; slider.value = ev.current;
    $("evOut").textContent = fmtEV(ev.current, ev.step) + " EV";
  }

  // HUD.
  if (st) {
    const mode = st.exposureMode ? st.exposureMode.current : "";
    $("hudMode").textContent = MODE_ABBR[mode] || mode || "—";
    $("hudShutter").textContent = st.shutterSpeed ? st.shutterSpeed.current : "—";
    $("hudF").textContent = st.fNumber ? st.fNumber.current : "—";
    $("hudIso").textContent = st.isoSpeedRate ? st.isoSpeedRate.current : "—";
    $("hudEv").textContent = ev ? fmtEV(ev.current, ev.step) : "";
    const focus = st.focusStatus && st.focusStatus !== "Not Focusing" ? st.focusStatus : "";
    $("hudFocus").textContent = focus ? "● " + focus : "";
    $("hudFocus").className = "focus" + (focus === "Focused" ? " ok" : "");
  }

  // Zoom meter.
  const zoomable = st && st.zoomPosition !== undefined && has("actZoom");
  $("zoomMeter").classList.toggle("on", !!zoomable);
  if (zoomable) $("zoomMeter").querySelector("i").style.width = st.zoomPosition + "%";

  // Top bar pills.
  const bat = st && st.battery;
  $("pillBattery").hidden = !bat || bat.percent < 0;
  if (bat && bat.percent >= 0) {
    $("pillBattery").querySelector(".battery").classList.toggle("low", bat.percent <= 20);
    $("pillBattery").querySelector(".battery i").style.width = bat.percent + "%";
    $("pillBattery").querySelector("strong").textContent = bat.percent + "%";
  }
  const storage = st && st.storage;
  $("pillShots").hidden = !storage || storage.remainingShots < 0;
  if (storage) $("pillShots").querySelector("strong").textContent = storage.remainingShots.toLocaleString();

  // Controls.
  $("btnShutter").disabled = !st || !has("actTakePicture") || state.capturing;
  $("btnZoomIn").disabled = $("btnZoomOut").disabled = !has("actZoom");
  $("btnAF").disabled = !has("actHalfPressShutter");
  vf.classList.toggle("touch-af", has("setTouchAFPosition"));
  $("btnTlStart").disabled = !st || !has("actTakePicture");
}

// ── Exposure compensation ───────────────────────────────────────────────────

$("ev").addEventListener("input", () => {
  state.evDragging = true;
  const ev = state.status && state.status.exposureCompensation;
  if (ev) $("evOut").textContent = fmtEV(+$("ev").value, ev.step) + " EV";
});
$("ev").addEventListener("change", async () => {
  try {
    await api("PUT", "/api/settings/exposureCompensation", { value: +$("ev").value });
  } catch (e) {
    toast("Exposure compensation: " + e.message, "err");
  } finally {
    state.evDragging = false;
    pollStatus();
  }
});

// ── Shooting ─────────────────────────────────────────────────────────────────

async function capture() {
  if (state.capturing || $("btnShutter").disabled) return;
  state.capturing = true;
  $("btnShutter").classList.add("busy");
  $("btnShutter").disabled = true;
  const flash = $("flash");
  flash.classList.remove("go"); void flash.offsetWidth; flash.classList.add("go");
  try {
    const m = await api("POST", "/api/capture");
    toast("Saved " + m.path + " · " + fmtBytes(m.size), "ok");
    await loadMedia();
  } catch (e) {
    toast("Capture failed: " + e.message, "err");
  } finally {
    state.capturing = false;
    $("btnShutter").classList.remove("busy");
    renderStatus();
  }
}
$("btnShutter").addEventListener("click", capture);

// Press-and-hold helper: fires onDown on press and onUp on release/leave.
function holdable(el, onDown, onUp) {
  let held = false;
  el.addEventListener("pointerdown", (e) => {
    if (el.disabled) return;
    held = true;
    el.classList.add("held");
    el.setPointerCapture(e.pointerId);
    onDown();
  });
  const release = () => {
    if (!held) return;
    held = false;
    el.classList.remove("held");
    onUp();
  };
  el.addEventListener("pointerup", release);
  el.addEventListener("pointercancel", release);
  el.addEventListener("lostpointercapture", release);
}

const zoom = (direction, movement) =>
  api("POST", "/api/zoom", { direction, movement }).catch((e) => toast("Zoom: " + e.message, "err"));

holdable($("btnZoomIn"), () => zoom("in", "start"), () => zoom("in", "stop"));
holdable($("btnZoomOut"), () => zoom("out", "start"), () => zoom("out", "stop"));

function showAF(xPct, yPct, cls) {
  const box = $("afBox");
  box.style.left = xPct + "%";
  box.style.top = yPct + "%";
  box.className = "af-box " + cls;
}
let afHide = null;
function hideAF(delay) {
  clearTimeout(afHide);
  afHide = setTimeout(() => ($("afBox").className = "af-box"), delay);
}

holdable(
  $("btnAF"),
  async () => {
    clearTimeout(afHide);
    showAF(50, 50, "hunting");
    try {
      await api("POST", "/api/focus/half-press");
      showAF(50, 50, "locked");
    } catch (e) {
      toast("Focus: " + e.message, "err");
      hideAF(0);
    }
  },
  () => {
    api("POST", "/api/focus/release").catch(() => {});
    hideAF(500);
  },
);

// Tap to focus: map the click to the image's displayed box (object-fit: contain).
$("vf").addEventListener("click", async (e) => {
  if (!has("setTouchAFPosition") || !$("vf").classList.contains("streaming")) return;
  if (e.target.closest(".connect-card")) return;
  const img = $("live");
  const r = img.getBoundingClientRect();
  const iw = img.naturalWidth || 3, ih = img.naturalHeight || 2;
  const scale = Math.min(r.width / iw, r.height / ih);
  const w = iw * scale, h = ih * scale;
  const left = r.left + (r.width - w) / 2, top = r.top + (r.height - h) / 2;
  const x = ((e.clientX - left) / w) * 100, y = ((e.clientY - top) / h) * 100;
  if (x < 0 || x > 100 || y < 0 || y > 100) return;
  const vr = $("vf").getBoundingClientRect();
  const bx = ((e.clientX - vr.left) / vr.width) * 100, by = ((e.clientY - vr.top) / vr.height) * 100;
  clearTimeout(afHide);
  showAF(bx, by, "hunting");
  try {
    await api("POST", "/api/focus/touch", { x, y });
    showAF(bx, by, "locked");
  } catch (err) {
    toast("Focus: " + err.message, "err");
  }
  hideAF(1200);
});

// Grid & fullscreen.
function setGrid(on) {
  $("gridOverlay").classList.toggle("on", on);
  $("btnGrid").classList.toggle("on", on);
  localStorage.setItem("focal.grid", on ? "1" : "0");
}
$("btnGrid").addEventListener("click", () => setGrid(!$("gridOverlay").classList.contains("on")));
const toggleFull = () => (document.fullscreenElement ? document.exitFullscreen() : $("vf").requestFullscreen());
$("btnFull").addEventListener("click", toggleFull);

// ── Tabs ─────────────────────────────────────────────────────────────────────

for (const tab of document.querySelectorAll(".tab")) {
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab").forEach((t) => t.classList.toggle("active", t === tab));
    document.querySelectorAll(".pane").forEach((p) => p.classList.toggle("active", p.id === "pane-" + tab.dataset.tab));
  });
}

// ── Timelapse ────────────────────────────────────────────────────────────────

function updatePlan() {
  const interval = Math.max(0.5, +$("tlInterval").value || 0);
  const count = Math.max(0, Math.floor(+$("tlCount").value || 0));
  const fps = Math.max(1, +$("tlFps").value || 30);
  $("planFrames").textContent = count ? count.toLocaleString() : "∞";
  $("planShoot").textContent = count ? fmtDur(count * interval) : "∞";
  $("planClip").textContent = count ? (count / fps).toFixed(1) + "s" : "∞";
  document.querySelectorAll("#intervalChips .chip").forEach((c) => c.classList.toggle("on", +c.dataset.v === interval));
}
["tlInterval", "tlCount", "tlFps"].forEach((id) => $(id).addEventListener("input", updatePlan));
document.querySelectorAll("#intervalChips .chip").forEach((c) =>
  c.addEventListener("click", () => { $("tlInterval").value = c.dataset.v; updatePlan(); }),
);

$("btnTlStart").addEventListener("click", async () => {
  try {
    state.tl = await api("POST", "/api/timelapse", {
      intervalSeconds: +$("tlInterval").value,
      count: Math.floor(+$("tlCount").value || 0),
    });
    state.tlSetupView = false;
    $("renderFps").value = String(+$("tlFps").value || 30);
    renderTimelapse();
    pollTimelapse();
    toast("Timelapse started", "ok");
  } catch (e) {
    toast("Could not start: " + e.message, "err");
  }
});

$("btnTlStop").addEventListener("click", async () => {
  try {
    state.tl = await api("DELETE", "/api/timelapse");
    renderTimelapse();
  } catch (e) {
    toast(e.message, "err");
  }
});

$("btnRender").addEventListener("click", async () => {
  try {
    state.tl = await api("POST", "/api/timelapse/render", { fps: +$("renderFps").value });
    renderTimelapse();
    pollTimelapse();
  } catch (e) {
    toast("Render: " + e.message, "err");
  }
});

$("btnTlNew").addEventListener("click", () => {
  state.tlSetupView = true;
  renderTimelapse();
});

let tlTimer = null;
let lastCaptured = -1;

async function pollTimelapse() {
  clearTimeout(tlTimer);
  try {
    state.tl = await api("GET", "/api/timelapse");
    renderTimelapse();
    if (state.tl.captured !== lastCaptured) {
      lastCaptured = state.tl.captured;
      loadMedia();
    }
  } catch (_) { /* server restarting; keep trying */ }
  const busy = state.tl && (state.tl.running || (state.tl.render && state.tl.render.running));
  tlTimer = setTimeout(pollTimelapse, busy ? 1000 : 5000);
}

function renderTimelapse() {
  const tl = state.tl;
  const active = tl && tl.session && !state.tlSetupView;
  $("tlSetup").hidden = !!active;
  $("tlRun").hidden = !active;
  $("tabDot").hidden = !(tl && tl.running);
  $("tabDot").className = "dot busy";

  const badge = $("recBadge");
  badge.classList.toggle("on", !!(tl && tl.running));
  if (!active) return;

  $("tlRunTitle").textContent = tl.running ? "Capturing" : tl.error ? "Stopped with an error" : "Finished";
  $("tlCaptured").textContent = tl.captured;
  $("tlOf").textContent = tl.total ? "of " + tl.total : "frames";
  const frac = tl.total ? tl.captured / tl.total : tl.running ? 0.25 : 1;
  $("ringFg").style.strokeDashoffset = String(276.46 * (1 - Math.min(1, frac)));
  $("tlFailed").textContent = tl.failed;

  const remaining = tl.total ? Math.max(0, tl.total - tl.captured) * tl.intervalSeconds : Infinity;
  $("tlRemaining").textContent = tl.running ? fmtDur(remaining) : "—";

  const last = $("tlLast");
  last.classList.toggle("on", !!tl.lastFrame);
  if (tl.lastFrame) last.style.backgroundImage = `url("${tl.lastFrame.url}")`;

  $("tlWarn").textContent = tl.warning || "";
  $("tlWarn").classList.toggle("on", !!tl.warning);
  $("tlErr").textContent = tl.error || "";
  $("tlErr").classList.toggle("on", !!tl.error);
  $("btnTlStop").hidden = !tl.running;

  const r = tl.render;
  $("tlRender").hidden = tl.running || tl.captured === 0;
  const rendering = !!(r && r.running);
  $("btnRender").disabled = rendering;
  $("btnRender").innerHTML = rendering ? '<span class="spinner"></span> Rendering…' : r && r.output ? "Render again" : "Render MP4";
  $("renderErr").textContent = (r && r.error) || "";
  $("renderErr").classList.toggle("on", !!(r && r.error));
  const ok = $("renderOk");
  ok.classList.toggle("on", !!(r && r.output && !rendering));
  if (r && r.output && !rendering) {
    ok.innerHTML = "";
    ok.append("Video ready · " + fmtBytes(r.output.size) + " · ");
    const a = document.createElement("a");
    a.href = "#";
    a.textContent = "Play";
    a.onclick = (e) => { e.preventDefault(); openLightbox(r.output); };
    ok.append(a);
  }
  tickTimelapse();
}

// Countdown and elapsed time update between polls.
function tickTimelapse() {
  const tl = state.tl;
  if (!tl || !tl.session) return;
  const now = Date.now();
  const start = tl.startedAt ? Date.parse(tl.startedAt) : now;
  const end = tl.finishedAt ? Date.parse(tl.finishedAt) : now;
  $("tlElapsed").textContent = fmtDur((end - start) / 1000);
  let next = "—";
  if (tl.running && tl.nextShotAt) {
    const s = (Date.parse(tl.nextShotAt) - now) / 1000;
    next = s <= 0.2 ? "now" : s.toFixed(s < 10 ? 1 : 0) + "s";
  }
  $("tlNext").textContent = next;
  $("recText").textContent = "TL " + tl.captured + (tl.total ? "/" + tl.total : "") + (tl.running && next !== "—" ? " · " + next : "");
}
setInterval(tickTimelapse, 200);

// ── Library ──────────────────────────────────────────────────────────────────

async function loadMedia() {
  try {
    state.media = await api("GET", "/api/media");
  } catch (_) {
    return;
  }
  renderMedia();
}

function renderMedia() {
  // Collapse each timelapse session into one tile; photos and videos stand alone.
  const tiles = [];
  const sessions = new Map();
  for (const m of state.media) {
    if (m.session && m.kind === "photo") {
      let s = sessions.get(m.session);
      if (!s) {
        s = { kind: "session", latest: m, count: 0, key: "s:" + m.session, time: m.modTime };
        sessions.set(m.session, s);
        tiles.push(s);
      }
      s.count++;
    } else {
      tiles.push({ kind: m.kind, latest: m, key: m.path, time: m.modTime });
    }
  }
  const strip = $("strip");
  strip.innerHTML = "";
  const photos = state.media.filter((m) => m.kind === "photo").length;
  const videos = state.media.length - photos;
  $("galleryCount").textContent = state.media.length
    ? `${photos} photo${photos === 1 ? "" : "s"}${videos ? ` · ${videos} video${videos === 1 ? "" : "s"}` : ""}`
    : "";
  if (!tiles.length) {
    strip.innerHTML = '<div class="empty-gallery">Photos you take and timelapses you render appear here.</div>';
    return;
  }
  for (const t of tiles.slice(0, 60)) {
    const el = document.createElement("div");
    el.className = "thumb";
    if (state.seenMedia.size && !state.seenMedia.has(t.key)) el.classList.add("new");
    state.seenMedia.add(t.key);
    let tag = "";
    if (t.kind === "session") {
      el.style.backgroundImage = `url("${t.latest.url}")`;
      tag = `<span class="tag">Timelapse · ${t.count}</span>`;
    } else if (t.kind === "video") {
      el.style.background = "linear-gradient(135deg, #2a1d10, #3b1f2c)";
      tag = '<span class="tag video">▶ Video</span>';
    } else {
      el.style.backgroundImage = `url("${t.latest.url}")`;
    }
    el.innerHTML = tag;
    el.title = t.latest.path;
    el.addEventListener("click", () => openLightbox(t.latest));
    strip.appendChild(el);
  }
}

function openLightbox(m) {
  const body = $("lbBody");
  body.innerHTML = "";
  const el = document.createElement(m.kind === "video" ? "video" : "img");
  el.src = m.url;
  if (m.kind === "video") { el.controls = true; el.autoplay = true; el.loop = true; }
  body.appendChild(el);
  $("lbName").textContent = m.path + " · " + fmtBytes(m.size);
  $("lbDownload").href = m.url;
  $("lightbox").classList.add("on");
}
function closeLightbox() {
  $("lightbox").classList.remove("on");
  $("lbBody").innerHTML = "";
}
$("lbClose").addEventListener("click", closeLightbox);
$("lightbox").addEventListener("click", (e) => { if (e.target === $("lightbox")) closeLightbox(); });

// ── Keyboard ────────────────────────────────────────────────────────────────

document.addEventListener("keydown", (e) => {
  if (e.target.closest("input, select, textarea") || e.metaKey || e.ctrlKey || e.altKey) return;
  switch (e.key) {
    case " ": case "Enter":
      if (e.target.closest("button")) return; // let a focused button handle it
      e.preventDefault(); capture(); break;
    case "+": case "=": if (has("actZoom")) zoom("in", "1shot"); break;
    case "-": case "_": if (has("actZoom")) zoom("out", "1shot"); break;
    case "g": case "G": $("btnGrid").click(); break;
    case "f": case "F": toggleFull(); break;
    case "Escape": closeLightbox(); break;
  }
});

// Mouse clicks should not leave buttons focused, so Space keeps meaning "shoot".
document.addEventListener("pointerup", () => {
  if (document.activeElement && document.activeElement.tagName === "BUTTON") document.activeElement.blur();
});

document.addEventListener("visibilitychange", () => { if (!document.hidden && state.connected) pollStatus(); });

// ── Boot ────────────────────────────────────────────────────────────────────

$("btnConnect").addEventListener("click", () => connect(true));

(async function boot() {
  buildDials();
  setGrid(localStorage.getItem("focal.grid") === "1");
  updatePlan();
  renderStatus();
  loadMedia();
  pollTimelapse();
  try {
    const info = await api("GET", "/api/camera");
    if (info.connected) onConnected(info);
    else connect(false);
  } catch (_) {
    onDisconnected("focal server is not reachable");
  }
})();
