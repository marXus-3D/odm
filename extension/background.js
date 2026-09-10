// DM Integration -- service worker.
//
// The job here is narrow: notice a download Chrome is about to make, cancel
// it, and hand the URL to the DM daemon together with the credentials the tab
// would have used. Without the cookies, Referer and User-Agent, an
// authenticated download fetched by an outside process just gets a 403.

const HOST = "com.dm.host";

const DEFAULTS = {
  enabled: true,
  minSize: 1024 * 1024, // bytes; smaller files are not worth the round trip
  captureUnknownSize: true,
  skipExtensions: ["html", "htm", "xml", "json", "txt", "css", "js", "svg"],
  notify: true,
};

// URLs we deliberately let Chrome handle, so a fallback re-download does not
// bounce straight back into us.
const passthrough = new Set();

async function settings() {
  const s = await chrome.storage.sync.get(DEFAULTS);
  return { ...DEFAULTS, ...s };
}

function sendNative(msg) {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendNativeMessage(HOST, msg, (reply) => {
      const err = chrome.runtime.lastError;
      if (err) return reject(new Error(err.message));
      if (!reply) return reject(new Error("no reply from the DM native host"));
      if (!reply.ok) return reject(new Error(reply.error || "unknown DM error"));
      resolve(reply.data);
    });
  });
}

function notify(title, message) {
  chrome.notifications.create({
    type: "basic",
    iconUrl: chrome.runtime.getURL("icons/icon48.png"),
    title,
    message,
  }, () => void chrome.runtime.lastError);
}

// cookieHeader rebuilds the Cookie header the browser would have sent. The
// cookies API includes HttpOnly cookies, which is exactly why this works for
// logged-in downloads.
async function cookieHeader(url) {
  try {
    const jar = await chrome.cookies.getAll({ url });
    if (!jar.length) return "";
    return jar.map((c) => `${c.name}=${c.value}`).join("; ");
  } catch (e) {
    console.warn("DM: could not read cookies", e);
    return "";
  }
}

function extensionOf(name) {
  if (!name) return "";
  const base = name.split(/[\\/]/).pop() || "";
  const i = base.lastIndexOf(".");
  return i > 0 ? base.slice(i + 1).toLowerCase() : "";
}

function isFetchable(url) {
  return /^https?:\/\//i.test(url);
}

async function shouldCapture(item, cfg) {
  if (!cfg.enabled) return false;
  if (passthrough.has(item.url)) return false;
  if (!isFetchable(item.finalUrl || item.url)) return false;
  // Chrome reports -1 or 0 before the response headers are parsed.
  if (item.fileSize > 0 && item.fileSize < cfg.minSize) return false;
  if (item.fileSize <= 0 && !cfg.captureUnknownSize) return false;

  const ext = extensionOf(item.filename) || extensionOf(item.url.split("?")[0]);
  if (ext && cfg.skipExtensions.includes(ext)) return false;
  return true;
}

chrome.downloads.onCreated.addListener(async (item) => {
  const cfg = await settings();
  if (!(await shouldCapture(item, cfg))) return;

  const url = item.finalUrl || item.url;

  // Cancel first: every millisecond of delay is bytes Chrome writes to a file
  // we are about to abandon.
  try {
    await chrome.downloads.cancel(item.id);
    await chrome.downloads.erase({ id: item.id });
  } catch (e) {
    console.warn("DM: could not cancel download", e);
    return; // Chrome already finished it; leave it alone.
  }

  const filename = item.filename ? item.filename.split(/[\\/]/).pop() : "";
  try {
    await sendNative({
      type: "add",
      url,
      filename,
      referer: item.referrer || "",
      cookie: await cookieHeader(url),
      userAgent: navigator.userAgent,
    });
    if (cfg.notify) notify("Sent to DM", filename || url);
  } catch (e) {
    console.error("DM: handoff failed", e);
    if (cfg.notify) notify("DM unavailable", e.message + " -- downloading in Chrome instead");
    // Do not silently lose the user's download.
    passthrough.add(url);
    chrome.downloads.download({ url }, () => {
      setTimeout(() => passthrough.delete(url), 60_000);
      void chrome.runtime.lastError;
    });
  }
});

// --- context menu -----------------------------------------------------------

const MENU_ID = "dm-download";

chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.create({
    id: MENU_ID,
    title: "Download with DM",
    contexts: ["link", "image", "video", "audio", "selection"],
  }, () => void chrome.runtime.lastError);
});

chrome.contextMenus.onClicked.addListener(async (info, tab) => {
  if (info.menuItemId !== MENU_ID) return;
  const url = info.linkUrl || info.srcUrl || info.selectionText;
  if (!url || !isFetchable(url)) {
    notify("DM", "That is not a downloadable http(s) link.");
    return;
  }
  try {
    await sendNative({
      type: "add",
      url,
      referer: info.pageUrl || (tab && tab.url) || "",
      cookie: await cookieHeader(url),
      userAgent: navigator.userAgent,
    });
    notify("Sent to DM", url);
  } catch (e) {
    notify("DM error", e.message);
  }
});

// Let the popup and options page reuse the one-shot native channel.
chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (!msg || msg.scope !== "dm") return false;
  sendNative(msg.payload)
    .then((data) => sendResponse({ ok: true, data }))
    .catch((e) => sendResponse({ ok: false, error: e.message }));
  return true; // keep the channel open for the async reply
});
