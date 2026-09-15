// ODM Integration -- in-page video panel.
//
// The IDM-style floating button that appears over a video. It exists because
// a streaming site hands the browser a blob: URL backed by Media Source
// Extensions, so there is no file to right-click and nothing for the
// downloads API to intercept. The real address is the playlist the page
// fetched, which the service worker sees and passes to us here.

(() => {
  if (window.__dmPanelLoaded) return; // survive a re-injection
  window.__dmPanelLoaded = true;

  const MIN_SIZE = 120;      // ignore tracking pixels and tiny previews
  const REPOSITION_MS = 250;

  /** Media the service worker noticed this tab fetch. */
  let detected = [];
  /** Videos the user dismissed the panel for, so it stays dismissed. */
  const dismissed = new WeakSet();
  /** video element -> panel host node. */
  const panels = new Map();

  const isHttp = (u) => typeof u === "string" && /^https?:\/\//i.test(u);

  // --- choosing what a given <video> would actually download ---------------

  // titleName turns a page title into something usable as a filename. The
  // site's own name is trailing noise, and Windows rejects a fair few of the
  // characters a title may contain.
  function titleName(raw) {
    let s = (raw || "").trim();
    const cut = s.split(/\s+[|–—-]\s+/);
    if (cut.length > 1 && cut[0].length >= 8) s = cut[0];
    s = s.replace(/[<>:"|?*\\/]+/g, " ").replace(/\s+/g, " ").trim();
    s = s.replace(/[. ]+$/, "");
    return s.length > 120 ? s.slice(0, 120).trim() : s;
  }


  function directSource(video) {
    if (isHttp(video.currentSrc)) return video.currentSrc;
    if (isHttp(video.src)) return video.src;
    for (const s of video.querySelectorAll("source")) {
      const u = s.src || s.getAttribute("src");
      if (isHttp(u)) return u;
    }
    return null;
  }

  function pickTarget(video) {
    // This element's own source wins when it is fetchable, because it is
    // unambiguously the video the panel is sitting on. A playlist noticed
    // elsewhere on the page might belong to an ad or another player.
    const direct = directSource(video);
    if (direct) return { url: direct, kind: "file" };

    // No usable src means an MSE player feeding from a blob:, which is
    // exactly the case this panel exists for: the real address is the
    // playlist the page fetched.
    const playlist = detected.find((m) => m.kind === "hls");
    if (playlist) return { url: playlist.url, kind: "hls" };

    const other = detected.find((m) => m.kind !== "dash");
    if (other) return { url: other.url, kind: other.kind };
    return null;
  }

  // --- the panel -----------------------------------------------------------

  function buildPanel(video) {
    // A shadow root keeps the page's CSS from reaching in and wrecking this,
    // which on a media-heavy site it otherwise reliably does.
    const host = document.createElement("div");
    host.setAttribute("data-odm-panel", "");
    Object.assign(host.style, {
      position: "absolute",
      zIndex: "2147483647",
      pointerEvents: "auto",
      // Positioned for real in reposition(); this just keeps it off screen
      // until then so it never flashes in the top-left corner.
      top: "-9999px",
      left: "-9999px",
    });

    const root = host.attachShadow({ mode: "closed" });
    const style = document.createElement("style");
    style.textContent = `
      .bar {
        display: inline-flex; align-items: center; gap: 6px;
        font: 600 12px/1 system-ui, "Segoe UI", sans-serif;
        color: #eaf2ff; background: rgba(17, 22, 32, .92);
        border: 1px solid rgba(79,156,249,.7); border-radius: 8px;
        padding: 6px 8px; box-shadow: 0 4px 14px rgba(0,0,0,.4);
        backdrop-filter: blur(3px);
      }
      button {
        font: inherit; color: inherit; background: transparent;
        border: 0; cursor: pointer; padding: 2px 4px; border-radius: 5px;
        display: inline-flex; align-items: center; gap: 6px;
      }
      button:hover { background: rgba(79,156,249,.22); }
      .go .tri { color: #4f9cf9; font-size: 11px; }
      .x { opacity: .65; font-weight: 700; }
      .x:hover { opacity: 1; }
      .tag {
        font-size: 10px; text-transform: uppercase; letter-spacing: .5px;
        border: 1px solid rgba(79,156,249,.55); color: #9dc7ff;
        border-radius: 999px; padding: 1px 5px;
      }
      .bar.busy { opacity: .75; }
      .bar.ok { border-color: rgba(62,207,142,.8); }
      .bar.err { border-color: rgba(244,88,106,.9); }
    `;

    const bar = document.createElement("div");
    bar.className = "bar";

    const go = document.createElement("button");
    go.className = "go";
    go.innerHTML = `<span class="tri">&#9654;</span><span class="label">Download this video</span>`;

    const tag = document.createElement("span");
    tag.className = "tag";

    const close = document.createElement("button");
    close.className = "x";
    close.textContent = "×";
    close.title = "Hide for this video";

    bar.append(go, tag, close);
    root.append(style, bar);

    const label = go.querySelector(".label");

    go.addEventListener("click", async (e) => {
      e.preventDefault();
      e.stopPropagation();
      const target = pickTarget(video);
      if (!target) {
        bar.classList.add("err");
        label.textContent = "No downloadable source";
        return;
      }
      bar.classList.add("busy");
      label.textContent = "Sending to ODM...";
      try {
        const res = await chrome.runtime.sendMessage({
          scope: "odm",
          payload: {
            type: "add",
            url: target.url,
            kind: target.kind,
            referer: location.href,
            // A playlist has no name of its own -- master.m3u8 says nothing
            // about the video, and a signed endpoint says less -- so the
            // page's title is the fallback. A file the server names keeps
            // the server's name.
            title: titleName(document.title),
          },
        });
        bar.classList.remove("busy");
        if (res && res.ok) {
          bar.classList.add("ok");
          label.textContent = "Queued in ODM";
          setTimeout(() => {
            bar.classList.remove("ok");
            label.textContent = "Download this video";
          }, 2500);
        } else {
          bar.classList.add("err");
          label.textContent = (res && res.error) || "ODM is not running";
          go.title = (res && res.error) || "";
        }
      } catch (err) {
        bar.classList.remove("busy");
        bar.classList.add("err");
        label.textContent = "ODM is not running";
        go.title = String(err && err.message ? err.message : err);
      }
    });

    close.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      dismissed.add(video);
      removePanel(video);
    });

    host.__dmTag = tag;
    return host;
  }

  function removePanel(video) {
    const host = panels.get(video);
    if (host) {
      host.remove();
      panels.delete(video);
    }
  }

  function reposition(video, host) {
    const r = video.getBoundingClientRect();
    const hidden =
      r.width < MIN_SIZE || r.height < MIN_SIZE ||
      r.bottom < 0 || r.top > window.innerHeight ||
      r.right < 0 || r.left > window.innerWidth ||
      getComputedStyle(video).visibility === "hidden";

    host.style.display = hidden ? "none" : "block";
    if (hidden) return;
    host.style.top = `${window.scrollY + r.top + 10}px`;
    host.style.left = `${window.scrollX + r.left + 10}px`;
  }

  function refresh() {
    const videos = Array.from(document.querySelectorAll("video"));
    // Drop panels whose video is gone from the document.
    for (const v of Array.from(panels.keys())) {
      if (!v.isConnected) removePanel(v);
    }

    for (const video of videos) {
      if (dismissed.has(video)) continue;
      const target = pickTarget(video);
      if (!target) {
        removePanel(video);
        continue;
      }
      let host = panels.get(video);
      if (!host) {
        host = buildPanel(video);
        document.body.appendChild(host);
        panels.set(video, host);
      }
      host.__dmTag.textContent = target.kind === "hls" ? "hls" : "video";
      reposition(video, host);
    }
  }

  // Layout changes constantly on video pages; rAF-coalesce the work so this
  // never becomes the reason a site feels slow.
  let queued = false;
  function scheduleRefresh() {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      try {
        refresh();
      } catch (e) {
        /* a broken page must not take the panel down with it */
      }
    });
  }

  // --- wiring --------------------------------------------------------------

  chrome.runtime.onMessage.addListener((msg) => {
    if (msg && msg.scope === "odm-media-update") {
      detected = msg.media || [];
      scheduleRefresh();
    }
  });

  async function loadDetected() {
    try {
      const r = await chrome.runtime.sendMessage({ scope: "odm-media-self" });
      if (r && r.ok) detected = r.media || [];
    } catch {
      /* service worker asleep; the push message will arrive instead */
    }
    scheduleRefresh();
  }

  new MutationObserver(scheduleRefresh).observe(document.documentElement, {
    childList: true,
    subtree: true,
  });
  addEventListener("scroll", scheduleRefresh, { passive: true, capture: true });
  addEventListener("resize", scheduleRefresh, { passive: true });
  setInterval(scheduleRefresh, REPOSITION_MS);

  loadDetected();
})();
