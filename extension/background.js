// ODM Integration -- service worker.
//
// The job here is narrow: notice a download Chrome is about to make, cancel
// it, and hand the URL to the ODM daemon together with the credentials the tab
// would have used. Without the cookies, Referer and User-Agent, an
// authenticated download fetched by an outside process just gets a 403.

// Firefox exposes the promise-based WebExtension API as `browser`; Chrome
// defines only `chrome`, whose MV3 methods return promises too. Taking
// whichever exists gives one promise-based namespace on both, which matters
// because Firefox's own `chrome` alias is callback-only.
const api = globalThis.browser ?? globalThis.chrome;

const HOST = "com.odm.host";

const DEFAULTS = {
  enabled: true,
  minSize: 1024 * 1024, // bytes; smaller files are not worth the round trip
  captureUnknownSize: true,
  skipExtensions: ["html", "htm", "xml", "json", "txt", "css", "js", "svg"],
  notify: true,
};

// URLs we deliberately let Chrome handle, so a fallback re-download does not
// bounce straight back into us.
//
// Session storage rather than a Set in memory: the fallback download starts
// moments before the service worker may be torn down, and a worker that came
// back with an empty set would capture its own fallback and go round again.
const PASSTHROUGH_KEY = "passthrough";
const PASSTHROUGH_MS = 60_000;

async function markPassthrough(url) {
  const got = await api.storage.session.get(PASSTHROUGH_KEY);
  const all = got[PASSTHROUGH_KEY] || {};
  all[url] = Date.now() + PASSTHROUGH_MS;
  await api.storage.session.set({ [PASSTHROUGH_KEY]: all });
}

async function isPassthrough(...urls) {
  const got = await api.storage.session.get(PASSTHROUGH_KEY);
  const all = got[PASSTHROUGH_KEY] || {};
  const now = Date.now();
  let expired = false;
  for (const [u, deadline] of Object.entries(all)) {
    if (deadline <= now) {
      delete all[u];
      expired = true;
    }
  }
  if (expired) await api.storage.session.set({ [PASSTHROUGH_KEY]: all });
  return urls.some((u) => u && all[u]);
}

async function settings() {
  const s = await api.storage.sync.get(DEFAULTS);
  return { ...DEFAULTS, ...s };
}

// --- native messaging -------------------------------------------------------
//
// One long-lived port, not a sendNativeMessage per download. Chrome starts a
// fresh host process for every one-shot message, so a burst of downloads
// becomes a burst of processes and the ones that lose that race come back as
// "Specified native messaging host not found" even though the host is
// installed and working. A single port is a single process, and the host
// keeps its daemon connection alive across messages.

const NATIVE_TIMEOUT = 15_000; // a host that has not answered by now is stuck
const PORT_IDLE_MS = 30_000;   // let the host exit when nothing is going on

let port = null;
const pending = []; // FIFO: the host answers in the order it was asked
let idleTimer = null;

function nativePort() {
  if (port) return port;

  const p = api.runtime.connectNative(HOST);
  port = p;

  p.onMessage.addListener((reply) => {
    touchPort();
    const waiter = pending.shift();
    if (!waiter) return; // a reply to something that already timed out
    if (!reply) waiter.reject(new Error("no reply from the ODM native host"));
    else if (!reply.ok) waiter.reject(new Error(reply.error || "unknown ODM error"));
    else waiter.resolve(reply.data);
  });

  p.onDisconnect.addListener(() => {
    // Chrome reports why on runtime.lastError, Firefox on the port itself.
    const err = p.error || api.runtime.lastError;
    const msg = (err && err.message) || "the ODM native host disconnected";
    if (port === p) port = null;
    while (pending.length) pending.shift().reject(new Error(msg));
  });

  touchPort();
  return p;
}

// touchPort drops the port once it has been quiet for a while, so an idle
// browser is not holding a host process open all day.
function touchPort() {
  clearTimeout(idleTimer);
  idleTimer = setTimeout(() => {
    if (pending.length) return touchPort();
    const p = port;
    port = null;
    if (p) p.disconnect();
  }, PORT_IDLE_MS);
}

function sendNative(msg) {
  return new Promise((resolve, reject) => {
    let settled = false;
    const waiter = {
      resolve(v) { if (!settled) { settled = true; clearTimeout(timer); resolve(v); } },
      reject(e) { if (!settled) { settled = true; clearTimeout(timer); reject(e); } },
    };
    // A timed-out waiter stays in the queue so later replies keep lining up
    // with the requests that are still waiting; its callbacks are no-ops.
    const timer = setTimeout(
      () => waiter.reject(new Error("the ODM native host did not answer")),
      NATIVE_TIMEOUT);

    pending.push(waiter);
    try {
      nativePort().postMessage(msg);
      touchPort();
    } catch (e) {
      const i = pending.indexOf(waiter);
      if (i >= 0) pending.splice(i, 1);
      port = null;
      waiter.reject(e instanceof Error ? e : new Error(String(e)));
    }
  });
}

// notify reuses one notification id per kind, so a run of failures replaces
// the banner instead of stacking a tower of them.
function notify(title, message, id = "odm") {
  api.notifications.create(id, {
    type: "basic",
    iconUrl: api.runtime.getURL("icons/icon48.png"),
    title,
    message,
  }).catch(() => {}); // a notification that will not show is not worth reporting
}

// cookieHeader rebuilds the Cookie header the browser would have sent. The
// cookies API includes HttpOnly cookies, which is exactly why this works for
// logged-in downloads.
async function cookieHeader(url) {
  try {
    const jar = await api.cookies.getAll({ url });
    if (!jar.length) return "";
    return jar.map((c) => `${c.name}=${c.value}`).join("; ");
  } catch (e) {
    console.warn("ODM: could not read cookies", e);
    return "";
  }
}

function extensionOf(name) {
  if (!name) return "";
  const base = name.split(/[\\/]/).pop() || "";
  const i = base.lastIndexOf(".");
  return i > 0 ? base.slice(i + 1).toLowerCase() : "";
}

// suggestedName is the name Chrome had worked out by the time onCreated
// fired, or "" when it had not worked one out yet.
//
// Send nothing rather than a guess: ODM treats a name it is given as final,
// and at this point Chrome usually has only a placeholder -- an empty string,
// or a temporary "Unconfirmed 123456.crdownload" it has not yet renamed.
// With no name ODM asks the server itself, which is the better answer.
function suggestedName(item) {
  const base = item.filename ? item.filename.split(/[\\/]/).pop() : "";
  if (!base) return "";
  if (/\.crdownload$/i.test(base)) return "";
  if (/^Unconfirmed \d+/i.test(base)) return "";
  return base;
}

function isFetchable(url) {
  return /^https?:\/\//i.test(url);
}

const PLAYLIST_MIMES = [
  "application/vnd.apple.mpegurl", "application/x-mpegurl",
  "application/mpegurl", "audio/mpegurl", "audio/x-mpegurl",
  "video/x-mpegurl", "application/dash+xml",
];

// isPlaylist reports whether a download is a playlist rather than a file.
//
// Its own size says nothing: a playlist is a few hundred bytes of text that
// stands for a whole video, so the size and extension filters have to be
// skipped for one or the video is never captured at all.
function isPlaylist(item) {
  const url = (item.finalUrl || item.url || "").toLowerCase();
  if (/\.m3u8|\.m3u(?![0-9a-z])|\.mpd(?![0-9a-z])/.test(url)) return true;
  return PLAYLIST_MIMES.includes((item.mime || "").toLowerCase());
}

// titleName turns a page title into something usable as a filename. The
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

// activeTabTitle is the title of the page the download almost certainly
// came from. A DownloadItem does not say which tab started it, and the one
// in front is the one the user just clicked in.
async function activeTabTitle() {
  try {
    const [tab] = await api.tabs.query({ active: true, currentWindow: true });
    return tab ? titleName(tab.title) : "";
  } catch (e) {
    return "";
  }
}

// REPLAY_GRACE_MS is how recently a download must have started for the
// onCreated event to be about a download that is really starting now.
const REPLAY_GRACE_MS = 60_000;

// isStarting separates a download Chrome is about to make from one it made
// weeks ago.
//
// Chrome replays onCreated for every item already in the download history
// when the service worker starts, which happens every time the browser is
// opened. Treating those as new is ruinous: the handler cancels them, erases
// them from history, and downloads the whole lot again. A finished download
// is not in_progress, and one that was interrupted by the browser closing
// started long before this moment.
function isStarting(item) {
  if (item.state !== "in_progress" || item.paused) return false;
  const started = Date.parse(item.startTime || "");
  if (Number.isNaN(started)) return true; // no usable time: assume it is new
  return Date.now() - started < REPLAY_GRACE_MS;
}

async function shouldCapture(item, cfg) {
  if (!cfg.enabled) return false;
  if (!isStarting(item)) return false;
  if (!isFetchable(item.finalUrl || item.url)) return false;
  if (await isPassthrough(item.url, item.finalUrl)) return false;

  // A playlist is tiny and stands for something large, so neither the size
  // floor nor the extension list applies to it.
  if (isPlaylist(item)) return true;

  // Chrome reports -1 or 0 before the response headers are parsed.
  if (item.fileSize > 0 && item.fileSize < cfg.minSize) return false;
  if (item.fileSize <= 0 && !cfg.captureUnknownSize) return false;

  const ext = extensionOf(suggestedName(item)) || extensionOf(item.url.split("?")[0]);
  if (ext && cfg.skipExtensions.includes(ext)) return false;
  return true;
}

api.downloads.onCreated.addListener(async (item) => {
  const cfg = await settings();
  if (!(await shouldCapture(item, cfg))) return;

  const url = item.finalUrl || item.url;
  const playlist = isPlaylist(item);

  // Cancel first: every millisecond of delay is bytes Chrome writes to a file
  // we are about to abandon.
  try {
    await api.downloads.cancel(item.id);
    await api.downloads.erase({ id: item.id });
  } catch (e) {
    console.warn("ODM: could not cancel download", e);
    return; // Chrome already finished it; leave it alone.
  }

  const filename = suggestedName(item);
  const title = await activeTabTitle();
  try {
    await sendNative({
      type: "add",
      url,
      filename,
      title,
      referer: item.referrer || "",
      cookie: await cookieHeader(url),
      userAgent: navigator.userAgent,
    });
    if (cfg.notify) notify("Sent to ODM", filename || url, "odm-sent");
  } catch (e) {
    console.error("ODM: handoff failed", e);

    // Handing a playlist back to Chrome is not a fallback. It saves a few
    // hundred bytes of text named index-f2-v1-a1.m3u8 and calls that the
    // video, which is worse than saving nothing, so say what went wrong
    // instead of producing a file the user cannot play.
    if (playlist) {
      notify("ODM unavailable",
        e.message + " -- the video was not downloaded", "odm-error");
      return;
    }

    if (cfg.notify) {
      notify("ODM unavailable",
        e.message + " -- downloading in Chrome instead", "odm-error");
    }
    // Do not silently lose the user's download. The mark has to be on disk
    // before Chrome is asked, or the new download's own onCreated can arrive
    // first and be captured.
    await markPassthrough(url);
    if (item.url !== url) await markPassthrough(item.url);
    api.downloads.download({ url }).catch(() => {});
  }
});

// --- context menu -----------------------------------------------------------

const MENU_ID = "odm-download";

api.runtime.onInstalled.addListener(() => {
  api.contextMenus.create({
    id: MENU_ID,
    title: "Download with ODM",
    contexts: ["link", "image", "video", "audio", "selection"],
    // The one call here that keeps its callback: contextMenus.create returns
    // the new id rather than a promise in both browsers, so there is nothing
    // to catch. Creating a menu that already exists is the only likely
    // failure and it does not matter.
  }, () => void api.runtime.lastError);
});

api.contextMenus.onClicked.addListener(async (info, tab) => {
  if (info.menuItemId !== MENU_ID) return;
  const url = info.linkUrl || info.srcUrl || info.selectionText;
  if (!url || !isFetchable(url)) {
    notify("ODM", "That is not a downloadable http(s) link.", "odm-error");
    return;
  }
  try {
    await sendNative({
      type: "add",
      url,
      title: titleName(tab && tab.title),
      referer: info.pageUrl || (tab && tab.url) || "",
      cookie: await cookieHeader(url),
      userAgent: navigator.userAgent,
    });
    notify("Sent to ODM", url, "odm-sent");
  } catch (e) {
    notify("ODM error", e.message, "odm-error");
  }
});

// Let the popup and options page reuse the one-shot native channel.
api.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (!msg || msg.scope !== "odm") return false;
  sendNative(msg.payload)
    .then((data) => sendResponse({ ok: true, data }))
    .catch((e) => sendResponse({ ok: false, error: e.message }));
  return true; // keep the channel open for the async reply
});

// --- streaming media detection ----------------------------------------------
//
// Streaming sites never expose the video as a file, so there is nothing for
// the downloads API to intercept. What they do is fetch a playlist. Watching
// for those requests is how the "download this video" panel knows there is a
// video worth offering.

const MEDIA_KEY = "detectedMedia"; // tabId -> [{url, kind, title, ts}]
const MEDIA_PER_TAB = 20;

function classify(url) {
  const path = url.split("?")[0].toLowerCase();
  if (path.endsWith(".m3u8") || path.endsWith(".m3u")) return "hls";
  if (path.endsWith(".mpd")) return "dash";
  return null;
}

async function readMedia() {
  const got = await api.storage.session.get(MEDIA_KEY);
  return got[MEDIA_KEY] || {};
}

async function recordMedia(tabId, url, kind) {
  if (tabId < 0) return;
  const all = await readMedia();
  const list = all[String(tabId)] || [];
  if (list.some((m) => m.url === url)) return;

  // A master playlist is the useful entry point; variant playlists fetched
  // by the player afterwards would just clutter the list.
  list.unshift({ url, kind, ts: Date.now() });
  all[String(tabId)] = list.slice(0, MEDIA_PER_TAB);
  await api.storage.session.set({ [MEDIA_KEY]: all });
  updateBadge(tabId, all[String(tabId)].length);
  pushToTab(tabId, all[String(tabId)]);
}

// pushToTab tells the in-page panel what we found, so the button can appear
// the moment the player asks for its playlist rather than on the next scroll.
function pushToTab(tabId, media) {
  // No content script in that tab is fine, and is what the rejection means.
  api.tabs.sendMessage(tabId, { scope: "odm-media-update", media })
    .catch(() => {});
}

function updateBadge(tabId, count) {
  // A tab that closed between the find and the badge rejects; that is fine.
  api.action.setBadgeText({ tabId, text: count ? String(count) : "" })
    .catch(() => {});
  api.action.setBadgeBackgroundColor({ tabId, color: "#4f9cf9" })
    .catch(() => {});
}

api.webRequest.onBeforeRequest.addListener(
  (details) => {
    const kind = classify(details.url);
    if (kind) void recordMedia(details.tabId, details.url, kind);
  },
  { urls: ["http://*/*", "https://*/*"] }
);

// Some CDNs serve playlists from extension-less URLs, so fall back to the
// content type the response actually declares.
api.webRequest.onHeadersReceived.addListener(
  (details) => {
    if (classify(details.url)) return; // already recorded by URL
    const ct = (details.responseHeaders || [])
      .find((h) => h.name.toLowerCase() === "content-type");
    if (!ct) return;
    const v = (ct.value || "").toLowerCase();
    if (v.includes("mpegurl")) {
      void recordMedia(details.tabId, details.url, "hls");
    } else if (v.includes("dash+xml")) {
      void recordMedia(details.tabId, details.url, "dash");
    }
  },
  { urls: ["http://*/*", "https://*/*"] },
  ["responseHeaders"]
);

// A new page means the old list is stale.
api.tabs.onUpdated.addListener(async (tabId, changeInfo) => {
  if (!changeInfo.url) return;
  const all = await readMedia();
  if (all[String(tabId)]) {
    delete all[String(tabId)];
    await api.storage.session.set({ [MEDIA_KEY]: all });
  }
  updateBadge(tabId, 0);
  pushToTab(tabId, []);
});

api.tabs.onRemoved.addListener(async (tabId) => {
  const all = await readMedia();
  if (all[String(tabId)]) {
    delete all[String(tabId)];
    await api.storage.session.set({ [MEDIA_KEY]: all });
  }
});

// The popup asks for a named tab's finds; a content script asks for its own.
api.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (!msg) return false;
  if (msg.scope === "odm-media") {
    (async () => {
      const all = await readMedia();
      sendResponse({ ok: true, media: all[String(msg.tabId)] || [] });
    })();
    return true;
  }
  if (msg.scope === "odm-media-self") {
    (async () => {
      const tabId = sender.tab && sender.tab.id;
      if (tabId === undefined || tabId < 0) {
        sendResponse({ ok: true, media: [] });
        return;
      }
      const all = await readMedia();
      sendResponse({ ok: true, media: all[String(tabId)] || [] });
    })();
    return true;
  }
  return false;
});
