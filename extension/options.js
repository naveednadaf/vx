let state = null;

const $ = (id) => document.getElementById(id);

function send(msg) {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendMessage(msg, (resp) => {
      if (chrome.runtime.lastError) return reject(new Error(chrome.runtime.lastError.message));
      if (!resp || !resp.ok) return reject(new Error((resp && resp.error) || "no response"));
      resolve(resp);
    });
  });
}

function fmtTime(iso) {
  if (!iso) return "—";
  try { return new Date(iso).toLocaleString(); } catch { return iso; }
}

function render() {
  const { status, settings } = state;
  $("api-port").value = settings.apiPort;
  $("daemon-state").textContent = status && status.ok
    ? `● running — xray ${status.xray_running ? "up" : "DOWN"}, api :${status.api_port}`
    : "● daemon offline (run `vx serve` in a terminal)";

  const body = $("groups-body");
  body.innerHTML = "";
  const groups = (status && status.groups) || [];
  $("table-empty").textContent = groups.length ? "" : "no groups yet — add some above";
  for (const g of groups) {
    const tr = document.createElement("tr");
    if (status.group === g.name) tr.className = "active";
    const srcSummary = (g.sources || []).map((s) => {
      if (s.type === "vless") return "link";
      if (s.type === "sub") return "sub";
      return "file";
    }).join(", ");
    tr.innerHTML = `
      <td>${g.name}${status.group === g.name ? " ●" : ""}</td>
      <td>${g.node_count}</td>
      <td>${srcSummary}</td>
      <td>${fmtTime(g.updated)}</td>
      <td class="actions"></td>`;
    const actions = tr.querySelector(".actions");

    const useBtn = document.createElement("button");
    useBtn.className = "ghost";
    useBtn.textContent = "Use";
    useBtn.addEventListener("click", async () => {
      try {
        const r = await send({ type: "group", name: g.name });
        state.status = r.status;
        render();
      } catch (e) { alert(e.message); }
    });
    actions.appendChild(useBtn);

    const pingBtn = document.createElement("button");
    pingBtn.className = "ghost";
    pingBtn.textContent = "Ping";
    pingBtn.addEventListener("click", async () => {
      pingBtn.disabled = true;
      try {
        const r = await send({ type: "ping", group: g.name });
        state.status = r.status;
        render();
      } catch (e) { alert(e.message); }
      finally { pingBtn.disabled = false; }
    });
    actions.appendChild(pingBtn);

    const delBtn = document.createElement("button");
    delBtn.className = "ghost danger";
    delBtn.textContent = "Delete";
    delBtn.addEventListener("click", async () => {
      if (!confirm(`Delete group "${g.name}"?`)) return;
      try {
        const r = await send({ type: "deleteGroup", name: g.name });
        state.status = r.status;
        render();
      } catch (e) { alert(e.message); }
    });
    actions.appendChild(delBtn);

    body.appendChild(tr);
  }
}

async function refresh() {
  state = await send({ type: "state" });
  render();
}

document.addEventListener("DOMContentLoaded", () => {
  refresh().catch((e) => alert(e.message));

  $("btn-save-port").addEventListener("click", async () => {
    const port = Number($("api-port").value);
    if (!port || port < 1024 || port > 65535) return alert("invalid port");
    try {
      await send({ type: "setSettings", apiPort: port });
      await refresh();
    } catch (e) { alert(e.message); }
  });

  $("btn-add").addEventListener("click", async () => {
    const group = $("new-group").value.trim();
    const raw = $("sources").value;
    $("add-warn").textContent = "";
    if (!group) return alert("group name required");
    const sources = raw.split("\n").map((s) => s.trim()).filter(Boolean);
    if (!sources.length) return alert("paste some vless:// links, subscription URLs, or file paths");
    try {
      const r = await send({ type: "add", group, sources });
      if (r.warnings && r.warnings.length) $("add-warn").textContent = r.warnings.join("\n");
      $("sources").value = "";
      state.status = r.status || state.status;
      render();
    } catch (e) { $("add-warn").textContent = e.message; }
  });

  $("btn-refresh").addEventListener("click", async () => {
    try {
      const r = await send({ type: "refresh" });
      if (r.warnings && r.warnings.length) $("add-warn").textContent = r.warnings.join("\n");
      state.status = r.status || state.status;
      render();
    } catch (e) { $("add-warn").textContent = e.message; }
  });
});