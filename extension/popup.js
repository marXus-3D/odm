// The popup keeps one long-lived native port open while it is visible, so
// polling for progress does not respawn the host process every tick.

const list = document.getElementById("list");
let port = null;
let timer = null;
let uiBase = "";

function human(n) {
  if (n === undefined || n === null || n < 0) return "?";
  const u = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(1)) + " " + u[i];
}

function message(text) {
  list.textContent = "";
  const d = document.createElement("div");
  d.className = "msg";
  d.textContent = text;
  list.appendChild(d);
}

function render(downloads) {
  if (!downloads || !downloads.length) {
    message("No downloads yet.");
    return;
  }
  list.textContent = "";
  for (const r of downloads.slice(0, 25)) {
    const row = document.createElement("div");
    row.className = "row " + r.state;

    const nm = document.createElement("div");
    nm.className = "name";
    nm.textContent = r.filename || r.url;
    row.appendChild(nm);

    const bar = document.createElement("div");
    bar.className = "bar";
    const fill = document.createElement("i");
    const pct = r.size > 0 ? Math.min(100, (r.downloaded / r.size) * 100) : (r.state === "done" ? 100 : 0);
    fill.style.width = pct + "%";
    bar.appendChild(fill);
    row.appendChild(bar);

    const meta = document.createElement("div");
    meta.className = "meta";
    for (const t of [r.state, `${human(r.downloaded)} / ${r.size > 0 ? human(r.size) : "?"}`]) {
      const s = document.createElement("span");
      s.textContent = t;
      meta.appendChild(s);
    }
    row.appendChild(meta);
    list.appendChild(row);
  }
}

function connect() {
  try {
    port = chrome.runtime.connectNative("com.dm.host");
  } catch (e) {
    message("Native host not installed. Run dm-setup.");
    return;
  }
  port.onMessage.addListener((reply) => {
    if (!reply || !reply.ok) {
      message((reply && reply.error) || "DM is not responding.");
      return;
    }
    if (reply.data && reply.data.url) { uiBase = reply.data.url; return; }
    if (reply.data && reply.data.downloads) render(reply.data.downloads);
  });
  port.onDisconnect.addListener(() => {
    const e = chrome.runtime.lastError;
    message(e ? e.message : "Disconnected from DM.");
    clearInterval(timer);
    port = null;
  });

  port.postMessage({ type: "openUI" });
  const poll = () => port && port.postMessage({ type: "state" });
  poll();
  timer = setInterval(poll, 1000);
}

document.getElementById("openUI").onclick = () => {
  chrome.tabs.create({ url: uiBase || "http://127.0.0.1:9111/" });
};
document.getElementById("opts").onclick = () => chrome.runtime.openOptionsPage();

window.addEventListener("unload", () => {
  clearInterval(timer);
  if (port) port.disconnect();
});

connect();
