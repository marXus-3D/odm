// Draft switcher: a small pill group fixed to the bottom of every landing
// page draft. Left/right arrow keys move between drafts as well.
(function () {
  var names = ["Segments", "Datasheet", "Poster", "Fluent", "Essay", "Blueprint", "Ledger", "Swiss", "Industrial",
    "Auth", "Spotlight", "Glass", "SaaS", "Bento", "Terminal", "Sticker", "Serif", "Win95", "Aurora"];
  var total = names.length;
  var m = location.pathname.match(/\/([1-9][0-9]?)\/?$/);
  var current = m ? +m[1] : 1;

  var css = document.createElement("style");
  css.textContent =
    ".dm-switch{position:fixed;left:50%;bottom:18px;transform:translateX(-50%);z-index:9999;" +
    "display:flex;align-items:center;gap:2px;padding:5px 6px 5px 12px;border-radius:999px;" +
    "background:rgba(20,22,27,.86);color:#e6e9ef;backdrop-filter:blur(10px);-webkit-backdrop-filter:blur(10px);" +
    "box-shadow:0 8px 30px rgba(0,0,0,.35),inset 0 0 0 1px rgba(255,255,255,.08);" +
    "font:500 12px/1 system-ui,-apple-system,Segoe UI,Roboto,sans-serif;letter-spacing:.01em}" +
    ".dm-switch span{margin-right:6px;opacity:.72;white-space:nowrap}" +
    ".dm-switch a{display:grid;place-items:center;width:24px;height:24px;border-radius:999px;" +
    "color:inherit;text-decoration:none;font-weight:600;font-size:11px;transition:background .15s}" +
    ".dm-switch a:hover{background:rgba(255,255,255,.1)}" +
    ".dm-switch a:focus-visible{outline:2px solid #4f9cf9;outline-offset:1px}" +
    ".dm-switch a[aria-current]{background:#e6e9ef;color:#14161b}" +
    "@media (max-width:900px){.dm-switch span{display:none}.dm-switch{padding-left:6px}}" +
    "@media (prefers-reduced-motion:reduce){.dm-switch a{transition:none}}";
  document.head.appendChild(css);

  var nav = document.createElement("nav");
  nav.className = "dm-switch";
  nav.setAttribute("aria-label", "Landing page drafts");
  var label = document.createElement("span");
  label.textContent = "Draft " + current + " of " + total + ", " + names[current - 1];
  nav.appendChild(label);
  for (var i = 1; i <= total; i++) {
    var a = document.createElement("a");
    a.href = "/" + i;
    a.textContent = i;
    a.title = names[i - 1];
    if (i === current) a.setAttribute("aria-current", "page");
    nav.appendChild(a);
  }
  document.body.appendChild(nav);

  document.addEventListener("keydown", function (e) {
    if (e.target && /input|textarea|select/i.test(e.target.tagName)) return;
    if (e.key === "ArrowRight" && current < total) location.href = "/" + (current + 1);
    if (e.key === "ArrowLeft" && current > 1) location.href = "/" + (current - 1);
  });
})();
