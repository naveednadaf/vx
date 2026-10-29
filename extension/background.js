// vx background service worker: talks to the local control API and drives
// chrome.proxy. Per-tab control approximated by re-applying proxy state on
// tab activation (Chrome's proxy API is profile-wide).

const DEFAULTS = { apiPort: 18082, httpPort: 18080, master: true };

let cachedStatus = null;
let tabOverrides = null; // {tabId: bool} — explicit per-tab decisions

async function settings() {
  return chrome.storage.sync.get(DEFAULTS);
}

async function overrides() {
  if (!tabOverrides) {
    const s = await chrome.storage.session.get("tabOverrides");
    tabOverrides = s.tabOverrides || {};
  }
  return tabOverrides;
}

async function saveOverrides() {
  await chrome.storage.session.set({ tabOverrides: tabOverrides || {} });
}

async function apiCall(method, path, body, timeoutMs = 1200) {
  const { apiPort } = await settings();
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const resp = await fetch(`http://127.0.0.1:${apiPort}${path}`, {
      method,
      headers: { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: ctrl.signal,
    });
    const data = await resp.json().catch(() => ({}));
    if (!resp.ok) throw new Error(data.error || `HTTP ${resp.status}`);
    return data;
  } finally {
    clearTimeout(t);
  }
}

async function fetchStatus(silent = false) {
  try {
    cachedStatus = await apiCall("GET", "/status");
    await chrome.storage.session.set({ cachedStatus });
    if (cachedStatus.http_port) {
      await chrome.storage.sync.set({ httpPort: cachedStatus.http_port });
    }
    return cachedStatus;
  } catch (e) {
    if (!silent) console.warn("vx status failed:", e.message);
    const s = await chrome.storage.session.get("cachedStatus");
    cachedStatus = s.cachedStatus || null;
    return cachedStatus;
  }
}

async function applyProxyForTab(tabId) {
  const { master, httpPort } = await settings();
  const ov = await overrides();
  const useProxy = Object.prototype.hasOwnProperty.call(ov, tabId) ? ov[tabId] : master;
  if (useProxy) {
    const port = (cachedStatus && cachedStatus.http_port) || httpPort;
    await chrome.proxy.settings.set({
      value: {
        mode: "fixed_servers",
        rules: {
          singleProxy: {
            scheme: "http",
            host: "127.0.0.1",
            port: Number(port),
          },
          bypassList: ["localhost", "127.0.0.1", "<local>"],
        },
      },
      scope: "regular",
    });
  } else {
    await chrome.proxy.settings.set({ value: { mode: "direct" }, scope: "regular" });
  }
}

async function activeTabId() {
  const tabs = await chrome.tabs.query({ active: true, currentWindow: true });
  return tabs[0] ? tabs[0].id : null;
}

async function applyActive() {
  const id = await activeTabId();
  if (id !== null) await applyProxyForTab(id);
}

// --- lifecycle ----------------------------------------------------------------

chrome.runtime.onInstalled.addListener(async () => {
  await fetchStatus(true);
  await applyActive();
});

chrome.runtime.onStartup.addListener(async () => {
  await fetchStatus(true);
  await applyActive();
});

chrome.tabs.onActivated.addListener((info) => {
  applyProxyForTab(info.tabId).catch(() => {});
});

chrome.windows.onFocusChanged.addListener((winId) => {
  if (winId !== chrome.windows.WINDOW_ID_NONE) applyActive().catch(() => {});
});

chrome.tabs.onRemoved.addListener((tabId) => {
  overrides().then((ov) => {
    if (Object.prototype.hasOwnProperty.call(ov, tabId)) {
      delete ov[tabId];
      saveOverrides();
    }
  });
});

chrome.alarms.create("vx-poll", { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === "vx-poll") fetchStatus(true);
});

// --- message API --------------------------------------------------------------

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  handle(msg)
    .then((data) => sendResponse({ ok: true, ...data }))
    .catch((e) => sendResponse({ ok: false, error: String(e.message || e) }));
  return true; // async response
});

async function handle(msg) {
  switch (msg.type) {
    case "state": {
      if (!cachedStatus) {
        const s = await chrome.storage.session.get("cachedStatus");
        cachedStatus = s.cachedStatus || null;
      }
      const [status, s, ov, tabs] = await Promise.all([
        cachedStatus ? Promise.resolve(cachedStatus) : fetchStatus(true),
        settings(),
        overrides(),
        chrome.tabs.query({ active: true, currentWindow: true }),
      ]);
      // If we served from cache, trigger a non-blocking background refresh
      if (cachedStatus) {
        fetchStatus(true).catch(() => {});
      }
      const tab = tabs[0] || null;
      const override = tab && Object.prototype.hasOwnProperty.call(ov, tab.id) ? ov[tab.id] : null;
      return { status: status || cachedStatus, settings: s, tab, tabOverride: override };
    }
    case "master": {
      await chrome.storage.sync.set({ master: !!msg.value });
      await applyActive();
      return {};
    }
    case "tab": {
      const id = msg.tabId != null ? msg.tabId : await activeTabId();
      if (id == null) throw new Error("no active tab");
      const ov = await overrides();
      if (msg.value === null) delete ov[id];
      else ov[id] = !!msg.value;
      await saveOverrides();
      await applyProxyForTab(id);
      return { tabOverride: msg.value === null ? null : !!msg.value };
    }
    case "mode": {
      const status = await apiCall("POST", "/mode", { mode: msg.mode, node: msg.node ?? -1 });
      cachedStatus = status;
      return { status };
    }
    case "group": {
      const status = await apiCall("POST", "/group", { name: msg.name });
      cachedStatus = status;
      await applyActive();
      return { status };
    }
    case "ping": {
      const q = msg.group ? `?group=${encodeURIComponent(msg.group)}` : "";
      const status = await apiCall("POST", `/ping${q}`, {}, 45000);
      cachedStatus = status;
      return { status };
    }
    case "add": {
      const out = await apiCall("POST", "/add", { group: msg.group, sources: msg.sources });
      cachedStatus = out.status || cachedStatus;
      return out;
    }
    case "deleteGroup": {
      const status = await apiCall("DELETE", `/group/${encodeURIComponent(msg.name)}`);
      cachedStatus = status;
      return { status };
    }
    case "refresh": {
      const q = msg.group ? `?group=${encodeURIComponent(msg.group)}` : "";
      const out = await apiCall("POST", `/refresh${q}`, {});
      cachedStatus = out.status || cachedStatus;
      return out;
    }
    case "setSettings": {
      const patch = {};
      if (msg.apiPort) patch.apiPort = Number(msg.apiPort);
      if (msg.master != null) patch.master = !!msg.master;
      await chrome.storage.sync.set(patch);
      await fetchStatus(true);
      await applyActive();
      return {};
    }
    default:
      throw new Error(`unknown message ${msg.type}`);
  }
}