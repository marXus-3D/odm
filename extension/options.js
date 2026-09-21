// See background.js: `browser` on Firefox, `chrome` on Chrome.
const api = globalThis.browser ?? globalThis.chrome;

const DEFAULTS = {
  enabled: true,
  minSize: 1024 * 1024,
  captureUnknownSize: true,
  skipExtensions: ["html", "htm", "xml", "json", "txt", "css", "js", "svg"],
  notify: true,
};

const $ = (id) => document.getElementById(id);

async function load() {
  const cfg = { ...DEFAULTS, ...(await api.storage.sync.get(DEFAULTS)) };
  $("enabled").checked = cfg.enabled;
  $("minSize").value = (cfg.minSize / (1024 * 1024)).toFixed(1);
  $("captureUnknownSize").checked = cfg.captureUnknownSize;
  $("skipExtensions").value = cfg.skipExtensions.join(", ");
  $("notify").checked = cfg.notify;
}

$("save").onclick = async () => {
  const mib = parseFloat($("minSize").value);
  await api.storage.sync.set({
    enabled: $("enabled").checked,
    minSize: Math.max(0, Math.round((isNaN(mib) ? 1 : mib) * 1024 * 1024)),
    captureUnknownSize: $("captureUnknownSize").checked,
    skipExtensions: $("skipExtensions").value
      .split(",")
      .map((s) => s.trim().replace(/^\./, "").toLowerCase())
      .filter(Boolean),
    notify: $("notify").checked,
  });
  $("saved").classList.add("show");
  setTimeout(() => $("saved").classList.remove("show"), 1400);
};

load();
