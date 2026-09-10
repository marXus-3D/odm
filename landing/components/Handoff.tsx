"use client";

import { useEffect, useRef } from "react";
import gsap from "gsap";
import { ScrollTrigger } from "gsap/ScrollTrigger";

// Fig. 3: browser, extension, DM. A single signal travels the path when the
// figure comes into view, then the drawing is still.
export default function Handoff() {
  const ref = useRef<SVGSVGElement>(null);

  useEffect(() => {
    const svg = ref.current;
    if (!svg) return;
    gsap.registerPlugin(ScrollTrigger);
    const mm = gsap.matchMedia();

    mm.add("(prefers-reduced-motion: no-preference)", () => {
      const dot = svg.querySelector<SVGCircleElement>("[data-signal]");
      if (!dot) return;
      const tl = gsap.timeline({
        scrollTrigger: { trigger: svg, start: "top 80%", once: true },
      });
      tl.set(dot, { attr: { cx: 150 }, opacity: 1 })
        .to(dot, { attr: { cx: 205 }, duration: 0.5, ease: "power1.inOut" })
        .to(dot, { attr: { cx: 355 }, duration: 0.01 }, "+=0.35")
        .to(dot, { attr: { cx: 410 }, duration: 0.5, ease: "power1.inOut" })
        .to(dot, { opacity: 0, duration: 0.3 });
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
        aria-label="Browser, extension and DM as three boxes joined by a signal path"
      >
        <g fill="none" stroke="var(--line)" strokeWidth="1.1">
          <rect x="0" y="30" width="150" height="60" />
          <text className="strong" x="75" y="56" textAnchor="middle" stroke="none">Browser</text>
          <text x="75" y="74" textAnchor="middle" stroke="none">Chrome, Edge, Brave, Vivaldi</text>

          <rect x="205" y="30" width="150" height="60" />
          <text className="strong" x="280" y="56" textAnchor="middle" stroke="none">DM extension</text>
          <text x="280" y="74" textAnchor="middle" stroke="none">link and cookies</text>

          <rect x="410" y="30" width="150" height="60" stroke="var(--chalk)" />
          <text className="chalk" x="485" y="56" textAnchor="middle" stroke="none" fontWeight="600">DM</text>
          <text x="485" y="74" textAnchor="middle" stroke="none">eight connections</text>

          <line x1="150" y1="60" x2="205" y2="60" />
          <line x1="355" y1="60" x2="410" y2="60" />
          <circle data-signal cx="150" cy="60" r="3.5" fill="var(--chalk)" stroke="none" opacity="0" />

          <text x="280" y="118" textAnchor="middle" stroke="none">Native messaging. No network port is opened.</text>
        </g>
      </svg>
      <figcaption>
        <b>Fig. 3</b>
        Files behind a login download the way they would in the browser, only faster. Video
        pages get a button that sends the stream across.
      </figcaption>
    </figure>
  );
}
