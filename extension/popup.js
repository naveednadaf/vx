let state = null;
let busy = false;

const $ = (id) => document.getElementById(id);

function toast(msg, isError = true) {
  const t = $("toast");
  t.textContent = msg;
  t.style.borderColor = isError ? "#f8717155" : "#4ade8055";
  t.classList.remove("hidden");
  clearTimeout(toast._h);
  toast._h = setTimeout(() => t.classList.add("hidden"), 3200);
}

function send(msg) {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendMessage(msg, (resp) => {
      if (chrome.runtime.lastError) return reject(new Error(chrome.runtime.lastError.message));
      if (!resp || !resp.ok) return reject(new Error((resp && resp.error) || "no response"));
      resolve(resp);
    });
  });
}

function pingClass(ok, ms) {
  if (!ok || ms < 0) return "r";
  if (ms < 150) return "g";
  if (ms < 350) return "y";
  return "r";
}

function pingWidth(ms) {
  if (ms < 0) return 100;
  return Math.max(6, Math.min(100, 100 - ms / 5));
}

function render() {
  if (!state) return;
  const { status, settings, tab, tabOverride } = state;

  $("daemon-dot").className = "dot " + (status && status.ok ? "on" : "off");
  $("daemon-dot").title = status && status.ok ? "daemon running" : "daemon offline";

  $("master-toggle").checked = !!settings.master;
  $("master-sub").textContent = status && status.ok
    ? `local xray · :${status.http_port} · ${status.xray_running ? "xray up" : "xray DOWN"}`
    : "daemon offline — run `vx serve`";

  const gs = $("group-select");
  gs.innerHTML = "";
  if (status && status.groups) {
    for (const g of status.groups) {
      const opt = document.createElement("option");
      opt.value = g.name;
      opt.textContent = `${g.name} (${g.node_count})`;
      if (status.group === g.name) opt.selected = true;
      gs.appendChild(opt);
    }
  }
  if (gs.options.length === 0) {
    const opt = document.createElement("option");
    opt.value = "";
    opt.textContent = "no groups — open settings";
    gs.appendChild(opt);
  }

  const mode = status ? status.mode : "";
  document.querySelectorAll("#modes button").forEach((b) => {
    b.classList.toggle("active", b.dataset.mode === mode);
  });

  const hint = $("manual-hint");
  if (mode === "manual" && status && status.nodes) {
    const n = status.nodes.find((n) => n.index === status.manual_node);
    hint.textContent = n ? `pinned: ${n.name}` : "pinned node";
    hint.classList.remove("hidden");
  } else {
    hint.classList.add("hidden");
  }

  const nodes = (status && status.nodes) || [];
  $("node-count").textContent = nodes.length ? `${nodes.length}` : "";
  const box = $("nodes");
  box.innerHTML = "";
  if (nodes.length === 0) {
    box.innerHTML = `<div class="empty">${status && status.ok ? "no nodes in group" : "daemon offline"}</div>`;
  } else {
    const activeIdx = (status && typeof status.active_node === "number") ? status.active_node : -1;
    const sorted = [...nodes].sort((a, b) => {
      // In-use node always goes to the very top
      const aInUse = a.index === activeIdx;
      const bInUse = b.index === activeIdx;
      if (aInUse !== bInUse) return aInUse ? -1 : 1;
      if (a.ping_ok !== b.ping_ok) return a.ping_ok ? -1 : 1;
      if (a.ping_ok) return a.ping_ms - b.ping_ms;
      return a.index - b.index;
    });
    for (const n of sorted) {
      const el = document.createElement("div");
      const isInUse = n.index === activeIdx;
      const isManual = mode === "manual" && status.manual_node === n.index;
      let classes = "node";
      if (isInUse) classes += " in-use";
      if (isManual) classes += " selected";
      el.className = classes;
      const cls = pingClass(n.ping_ok, n.ping_ms);
      const label = n.ping_ok ? `${n.ping_ms} ms` : "fail";
      const inUseBadge = isInUse ? `<span class="badge-in-use">IN USE</span>` : "";
      el.innerHTML = `
        <div>
          <div class="node-name">${inUseBadge}${escapeHtml(n.name)}</div>
          <div class="bar"><span class="${cls}" style="width:${pingWidth(n.ping_ms)}%"></span></div>
        </div>
        <div class="node-ping">${label}</div>`;
      el.addEventListener("click", () => pinNode(n));
      box.appendChild(el);
    }
  }

  $("tab-toggle").checked = tabOverride == null ? !!settings.master : !!tabOverride;
  $("tab-title").textContent = tab ? tab.title || tab.url || `tab ${tab.id}` : "—";
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

async function refresh() {
  state = await send({ type: "state" });
  render();
  try {
    await chrome.storage.session.set({ cachedState: state });
  } catch (_) {}
}

async function pinNode(n) {
  if (busy) return;
  busy = true;
  try {
    const resp = await send({ type: "mode", mode: "manual", node: n.index });
    state.status = resp.status;
    render();
    toast(`pinned ${n.name}`, false);
  } catch (e) {
    toast(e.message);
  } finally {
    busy = false;
  }
}

document.addEventListener("DOMContentLoaded", async () => {
  // Read any pre-cached state from session storage for instant paint (<10ms)
  try {
    const pre = await chrome.storage.session.get("cachedState");
    if (pre && pre.cachedState) {
      state = pre.cachedState;
      render();
    }
  } catch (_) {}

  refresh().catch((e) => toast(e.message));
  setInterval(() => refresh().catch(() => {}), 4000);

  $("master-toggle").addEventListener("change", async (e) => {
    try {
      await send({ type: "master", value: e.target.checked });
      await refresh();
    } catch (err) { toast(err.message); e.target.checked = !e.target.checked; }
  });

  $("tab-toggle").addEventListener("change", async (e) => {
    try {
      await send({ type: "tab", value: e.target.checked });
      await refresh();
    } catch (err) { toast(err.message); }
  });

  $("group-select").addEventListener("change", async (e) => {
    if (!e.target.value) return;
    try {
      const resp = await send({ type: "group", name: e.target.value });
      state.status = resp.status;
      render();
      send({ type: "ping" }).then((r) => { state.status = r.status; render(); }).catch(() => {});
    } catch (err) { toast(err.message); }
  });

  document.querySelectorAll("#modes button").forEach((b) => {
    b.addEventListener("click", async () => {
      if (busy) return;
      busy = true;
      try {
        const resp = await send({ type: "mode", mode: b.dataset.mode });
        state.status = resp.status;
        render();
      } catch (e) { toast(e.message); } finally { busy = false; }
    });
  });

  $("btn-refresh").addEventListener("click", async () => {
    const btn = $("btn-refresh");
    if (busy) return;
    busy = true;
    btn.disabled = true;
    const old = btn.textContent;
    btn.textContent = "…";
    try {
      const groupName = $("group-select").value;
      const resp = await send({ type: "refresh", group: groupName || undefined });
      state.status = resp.status || state.status;
      render();
      if (resp.warnings && resp.warnings.length) {
        toast(resp.warnings[0], false);
      } else {
        const n = state.status && state.status.nodes ? state.status.nodes.length : 0;
        toast(`Links updated — ${n} nodes`, false);
      }
    } catch (e) { toast(e.message); }
    finally { btn.disabled = false; btn.textContent = old; busy = false; }
  });

  $("btn-ping").addEventListener("click", async () => {
    const btn = $("btn-ping");
    btn.disabled = true;
    btn.textContent = "…";
    try {
      const resp = await send({ type: "ping" });
      state.status = resp.status;
      render();
    } catch (e) { toast(e.message); }
    finally { btn.disabled = false; btn.textContent = "Ping"; }
  });

  $("btn-options").addEventListener("click", () => chrome.runtime.openOptionsPage());
});