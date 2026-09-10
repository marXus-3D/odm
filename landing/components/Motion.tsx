"use client";

import { useEffect } from "react";
import gsap from "gsap";
import { ScrollTrigger } from "gsap/ScrollTrigger";

// All motion on the page lives here and is driven by data attributes on
// server-rendered markup, so the HTML is complete without JavaScript and
// every animation is a `from`: the resting state is the state you see with
// motion off.
//
//   [data-hero]            children fade up in sequence on load
//   [data-reveal]          fades up when scrolled into view
//   [data-reveal][data-stagger]  same, one child after another
//   [data-count="6.5"][data-decimals="1"][data-suffix=" s"]  counts up
//   [data-split]           the range-splitting diagram, played once
//   [data-race]            the two measurement bars, drawn at real speed
//   [data-log] [data-line] log lines appear one by one
export default function Motion() {
  useEffect(() => {
    gsap.registerPlugin(ScrollTrigger);
    if (process.env.NODE_ENV !== "production") (window as unknown as { gsap: typeof gsap }).gsap = gsap;
    const mm = gsap.matchMedia();

    mm.add("(prefers-reduced-motion: no-preference)", () => {
      const ctx = gsap.context(() => {
        hero();
        counters();
        reveals();
        split();
        race();
        log();
      });
      return () => ctx.revert();
    });

    return () => mm.revert();
  }, []);

  return null;
}

function once(trigger: Element | string, start = "top 82%") {
  return { trigger, start, once: true };
}

function hero() {
  const tl = gsap.timeline({ defaults: { ease: "power3.out" } });
  tl.from(".light", { opacity: 0, duration: 1.8, ease: "power2.out" }, 0)
    .from("[data-hero] > *", { y: 22, opacity: 0, duration: 0.8, stagger: 0.09 }, 0.15)
    .from(".win", { y: 40, opacity: 0, duration: 1 }, 0.6)
    .from(".win .bar8 span", { width: 0, duration: 1.1, ease: "power2.inOut", stagger: 0.015 }, 1.1);

  // After the lanes fill, the unfinished rows keep creeping so the window
  // reads as live rather than as a picture.
  const live = gsap.utils.toArray<HTMLElement>(".win [data-live] .bar8 span");
  const tick = gsap.delayedCall(2.6, function creep() {
    live.forEach((s) => {
      const w = parseFloat(s.style.width);
      if (w < 100) gsap.to(s, { width: Math.min(100, w + gsap.utils.random(0.4, 1.6)) + "%", duration: 1, ease: "none" });
    });
    tick.restart(true);
  });
}

function counters() {
  gsap.utils.toArray<HTMLElement>("[data-count]").forEach((el) => {
    const end = parseFloat(el.dataset.count || "0");
    const decimals = parseInt(el.dataset.decimals || "0", 10);
    const suffix = el.dataset.suffix || "";
    const o = { v: 0 };
    gsap.to(o, {
      v: end,
      duration: 1.4,
      ease: "power2.out",
      scrollTrigger: once(el, "top 88%"),
      onUpdate: () => {
        el.textContent = o.v.toFixed(decimals) + suffix;
      },
    });
  });
}

function reveals() {
  gsap.utils.toArray<HTMLElement>("[data-reveal]").forEach((group) => {
    const items = group.hasAttribute("data-stagger") ? Array.from(group.children) : [group];
    gsap.from(items, {
      y: 24,
      opacity: 0,
      duration: 0.8,
      ease: "power3.out",
      stagger: 0.08,
      scrollTrigger: once(group),
    });
  });
}

// The file as a bar. One range, then eight, then the fills run; c4 is slow,
// and when c1 finishes it halves what c4 has left and takes the tail.
function split() {
  const root = document.querySelector<HTMLElement>("[data-split]");
  if (!root) return;
  const ranges = gsap.utils.toArray<HTMLElement>(".range:not(.new)", root);
  const fills = ranges.map((r) => r.querySelector<HTMLElement>(".fill")!);
  const fresh = root.querySelector<HTMLElement>(".range.new")!;
  const freshFill = fresh.querySelector<HTMLElement>(".fill")!;
  const steps = gsap.utils.toArray<HTMLElement>("[data-step]", root);
  const slow = 3;

  const activate = (i: number) => () => steps.forEach((s, j) => s.classList.toggle("on", j === i));

  const tl = gsap.timeline({ scrollTrigger: once(root, "top 70%"), defaults: { ease: "power2.inOut" } });

  tl.set(ranges, { left: 0, width: "100%", opacity: 0 })
    .set(ranges[0], { opacity: 1 })
    .set(fills, { width: 0 })
    .set(fresh, { opacity: 0, left: "45%", width: "5%" })
    .set(freshFill, { width: 0 })
    .call(activate(0))
    // eight ranges
    .to(ranges, { left: (i) => i * 12.5 + "%", width: "12.5%", opacity: 1, duration: 1, stagger: 0.04 }, 0.9)
    .call(activate(1), undefined, 1.9)
    // fills run; c4 is slow
    .to(fills.filter((_, i) => i !== slow), { width: "100%", duration: 2.6, ease: "none", stagger: { each: 0.05, from: "random" } }, 2.1)
    .to(fills[slow], { width: "20%", duration: 2.6, ease: "none" }, 2.1)
    // c1 finishes, splits c4's tail
    .call(activate(2), undefined, 4.8)
    .to(ranges[slow], { width: "7.5%", duration: 0.5 }, 4.9)
    .to(fills[slow], { width: "33%", duration: 0.5 }, 4.9)
    .to(fresh, { opacity: 1, duration: 0.3 }, 5.1)
    .to(freshFill, { width: "100%", duration: 1.2, ease: "none" }, 5.4)
    .to(fills[slow], { width: "60%", duration: 1.2, ease: "none" }, 5.4);
}

function race() {
  const root = document.querySelector<HTMLElement>("[data-race]");
  if (!root) return;
  const scale = 3.4; // 13.7 s plays in about four
  const lanes = gsap.utils.toArray<HTMLElement>(".lane", root);
  const tl = gsap.timeline({ scrollTrigger: once(root, "top 75%"), defaults: { ease: "none" } });
  lanes.forEach((lane) => {
    const seconds = parseFloat(lane.dataset.seconds || "0");
    const fill = lane.querySelector<HTMLElement>(".fill")!;
    const time = lane.querySelector<HTMLElement>("[data-time]")!;
    const o = { t: 0 };
    tl.fromTo(fill, { width: 0 }, { width: fill.style.width, duration: seconds / scale }, 0).to(
      o,
      {
        t: seconds,
        duration: seconds / scale,
        onUpdate: () => {
          time.textContent = o.t.toFixed(1) + " s";
        },
      },
      0,
    );
  });
}

function log() {
  const root = document.querySelector("[data-log]");
  if (!root) return;
  gsap.from(root.querySelectorAll("[data-line]"), {
    opacity: 0,
    x: -8,
    duration: 0.4,
    stagger: 0.16,
    ease: "power2.out",
    scrollTrigger: once(root, "top 80%"),
  });
}
