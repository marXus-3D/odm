// The popup keeps one long-lived native port open while it is visible, so
// polling for progress does not respawn the host process every tick.

// See background.js: `browser` on Firefox, `chrome` on Chrome, promises on
// both either way.
const api = globalThis.browser ?? globalThis.chrome;

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

// titleName turns a page title into something usable as a filename: the
// site's own name is trailing noise, and Windows rejects several of the
// characters a title may contain.
function titleName(raw) {
  let s = (raw || "").trim();
  const cut = s.split(/\s+[|–—-]\s+/);
  if (cut.length > 1 && cut[0].length >= 8) s = cut[0];
  s = s.replace(/[<>:"|?*\\/]+/g, " ").replace(/\s+/g, " ").trim();
  s = s.replace(/[. ]+$/, "");
  return s.length > 120 ? s.slice(0, 120).trim() : s;
}

// buildsItsOwnStream reports whether a page is one of the sites that feed
// their player from separately fetched audio and video parts. There is no
// single URL to download on those, so an empty media list is the truth
// rather than a failure.
function buildsItsOwnStream(url) {
  try {
    const h = new URL(url).hostname.toLowerCase().replace(/^(www|m|music)\./, "");
    return [
      "youtube.com", "youtube-nocookie.com", "youtu.be",
      "netflix.com", "spotify.com", "primevideo.com", "hulu.com",
      "disneyplus.com", "max.com", "tidal.com",
    ].includes(h);
  } catch {
    return false;
  }
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

// Firefox treats host_permissions in a Manifest V3 extension as optional, so
// <all_urls> is not granted at install. Without it the cookie jar is empty
// and webRequest sees nothing, and the extension looks installed and broken
// rather than unauthorised. Ask for it instead, from a click, which is the
// only context permissions.request may be called from.
async function checkPermissions() {
  const box = document.getElementById("perm");
  let granted = true;
  try {
    granted = await api.permissions.contains({ origins: ["<all_urls>"] });
  } catch {
    return; // no permissions API to ask: nothing useful to show
  }
  if (granted) return;

  box.classList.add("show");
  document.getElementById("grant").onclick = async () => {
    try {
      if (await api.permissions.request({ origins: ["<all_urls>"] })) {
        box.classList.remove("show");
        loadMedia().catch(() => {});
      }
    } catch (e) {
      message("Could not request permission: " + e.message);
    }
  };
}

// Streams the page fetched are not downloads Chrome ever started, so they
// have to be offered explicitly.
async function loadMedia() {
  const [tab] = await api.tabs.query({ active: true, currentWindow: true });
  if (!tab) return;
  const reply = await api.runtime.sendMessage({ scope: "odm-media", tabId: tab.id });
  const box = document.getElementById("media");
  box.textContent = "";
  if (!reply || !reply.ok || !reply.media.length) {
    // An empty panel on a video page reads as a bug. On the sites that
    // build their stream in the player there is nothing to offer, and
    // saying so is more use than showing nothing.
    if (buildsItsOwnStream(tab.url)) {
      const note = document.createElement("div");
      note.className = "msg";
      note.textContent =
        "This site assembles video in the player, so there is no file for " +
        "ODM to fetch. Sites that serve an .m3u8 playlist appear here.";
      box.appendChild(note);
    }
    return;
  }

  const head = document.createElement("div");
  head.className = "section";
  head.textContent = `Video on this page (${reply.media.length})`;
  box.appendChild(head);

  for (const m of reply.media) {
    const row = document.createElement("div");
    row.className = "media-row";

    const tag = document.createElement("span");
    tag.className = "tag";
    tag.textContent = m.kind;
    row.appendChild(tag);

    const u = document.createElement("div");
    u.className = "u";
    u.textContent = m.url;
    u.title = m.url;
    row.appendChild(u);

    const btn = document.createElement("button");
    btn.textContent = "Download";
    btn.onclick = async () => {
      btn.disabled = true;
      btn.textContent = "Sending...";
      const res = await api.runtime.sendMessage({
        scope: "odm",
        payload: {
          type: "add",
          url: m.url,
          kind: m.kind,
          referer: tab.url || "",
          // master.m3u8 is not a name, and a signed endpoint has none at
          // all. The daemon falls back to this when the URL and the server
          // between them cannot name the file.
          title: titleName(tab.title),
        },
      });
      btn.textContent = res && res.ok ? "Queued" : "Failed";
      if (!res || !res.ok) {
        btn.title = (res && res.error) || "unknown error";
        btn.disabled = false;
      }
    };
    row.appendChild(btn);
    box.appendChild(row);
  }
}

function connect() {
  try {
    port = api.runtime.connectNative("com.odm.host");
  } catch (e) {
    message("Native host not installed. Run odm-setup.");
    return;
  }
  port.onMessage.addListener((reply) => {
    if (!reply || !reply.ok) {
      message((reply && reply.error) || "ODM is not responding.");
      return;
    }
    if (reply.data && reply.data.url) { uiBase = reply.data.url; return; }
    if (reply.data && reply.data.downloads) render(reply.data.downloads);
  });
  port.onDisconnect.addListener(() => {
    // Chrome reports why on runtime.lastError, Firefox on the port itself.
    const e = port.error || api.runtime.lastError;
    message(e ? e.message : "Disconnected from ODM.");
    clearInterval(timer);
    port = null;
  });

  port.postMessage({ type: "openUI" });
  const poll = () => port && port.postMessage({ type: "state" });
  poll();
  timer = setInterval(poll, 1000);
}

document.getElementById("openUI").onclick = () => {
  api.tabs.create({ url: uiBase || "http://127.0.0.1:9111/" });
};
document.getElementById("opts").onclick = () => api.runtime.openOptionsPage();

window.addEventListener("unload", () => {
  clearInterval(timer);
  if (port) port.disconnect();
});

checkPermissions().catch(() => {});
loadMedia().catch(() => {});
connect();
