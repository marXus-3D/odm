"use client";

import { useEffect, useRef } from "react";
import gsap from "gsap";
import { ScrollTrigger } from "gsap/ScrollTrigger";

// Fig. 2: 13.7 s against 6.5 s, drawn to scale. The bars extend when the
// figure scrolls into view, at their real relative speeds, so the gap is
// something you watch open rather than read.
export default function Measurement() {
  const ref = useRef<SVGSVGElement>(null);

  useEffect(() => {
    const svg = ref.current;
    if (!svg) return;
    gsap.registerPlugin(ScrollTrigger);
    const mm = gsap.matchMedia();

    mm.add("(prefers-reduced-motion: no-preference)", () => {
      const slow = svg.querySelector<SVGRectElement>("[data-bar='slow']");
      const fast = svg.querySelector<SVGRectElement>("[data-bar='fast']");
      const slowLabel = svg.querySelector<SVGTextElement>("[data-time='slow']");
      const fastLabel = svg.querySelector<SVGTextElement>("[data-time='fast']");
      if (!slow || !fast || !slowLabel || !fastLabel) return;

      const scale = 3.4; // 13.7 s plays in about 4 s
      const tl = gsap.timeline({
        scrollTrigger: { trigger: svg, start: "top 80%", once: true },
        defaults: { ease: "none" },
      });
      const clock = { a: 0, b: 0 };
      tl.fromTo(slow, { attr: { width: 0 } }, { attr: { width: 478 }, duration: 13.7 / scale }, 0)
        .fromTo(fast, { attr: { width: 0 } }, { attr: { width: 226 }, duration: 6.5 / scale }, 0)
        .to(clock, {
          a: 13.7,
          duration: 13.7 / scale,
          onUpdate: () => {
            slowLabel.textContent = `${clock.a.toFixed(1)} s`;
          },
        }, 0)
        .to(clock, {
          b: 6.5,
          duration: 6.5 / scale,
          onUpdate: () => {
            fastLabel.textContent = `${clock.b.toFixed(1)} s`;
          },
        }, 0);
      return () => tl.kill();
    });

    return () => mm.revert();
  }, []);

  return (
    <figure>
      <svg
        ref={ref}
        className="drawing"
        viewBox="0 0 560 130"
        role="img"
        aria-label="Two bars, 13.7 seconds and 6.5 seconds, drawn to scale"
      >
        <defs>
          <pattern id="hatch2" width="7" height="7" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
            <line x1="0" y1="0" x2="0" y2="7" stroke="var(--line)" strokeWidth="1" opacity="0.7" />
          </pattern>
        </defs>
        <g fill="none" stroke="var(--line)" strokeWidth="1.1">
          <text x="0" y="22" stroke="none">1 connection</text>
          <rect x="0" y="30" width="480" height="22" />
          <rect data-bar="slow" x="1" y="31" width="478" height="20" fill="url(#hatch2)" stroke="none" />
          <text className="strong" data-time="slow" x="490" y="46" stroke="none" fontSize="16">13.7 s</text>

          <text x="0" y="82" stroke="none">8 connections, DM</text>
          <rect x="0" y="90" width="228" height="22" />
          <rect data-bar="fast" x="1" y="91" width="226" height="20" fill="var(--chalk)" stroke="none" />
          <text className="chalk" data-time="fast" x="238" y="106" stroke="none" fontSize="16" fontWeight="600">6.5 s</text>

          <line x1="228" y1="52" x2="228" y2="128" strokeDasharray="2 3" />
        </g>
      </svg>
      <figcaption>
        <b>Fig. 2</b>
        Most hosts cap each socket rather than each person, and TCP shares a busy link per
        flow. Eight flows, eight shares.
      </figcaption>
    </figure>
  );
}
