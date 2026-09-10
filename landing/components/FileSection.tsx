"use client";

import { useEffect, useRef } from "react";
import gsap from "gsap";

// Fig. 1: a section through a download in progress. On load the drawing is
// traced in once, the way a plotter would draw it; the hatched (written)
// areas fill in afterwards. This is the page's one piece of motion that is
// not answering a scroll.
export default function FileSection() {
  const ref = useRef<SVGSVGElement>(null);

  useEffect(() => {
    const svg = ref.current;
    if (!svg) return;
    const mm = gsap.matchMedia();

    mm.add("(prefers-reduced-motion: no-preference)", () => {
      const strokes = Array.from(svg.querySelectorAll<SVGGeometryElement>(".trace"));
      strokes.forEach((el) => {
        const len = el.getTotalLength();
        el.style.strokeDasharray = `${len}`;
        el.style.strokeDashoffset = `${len}`;
      });
      const tl = gsap.timeline({ defaults: { ease: "power2.inOut" } });
      tl.to(strokes, { strokeDashoffset: 0, duration: 0.9, stagger: 0.035 })
        .from(svg.querySelectorAll(".fill"), { opacity: 0, duration: 0.6, stagger: 0.05 }, "-=0.5")
        .from(svg.querySelectorAll("text"), { opacity: 0, duration: 0.5, stagger: 0.02 }, "-=0.6");
      return () => tl.kill();
    });

    return () => mm.revert();
  }, []);

  return (
    <figure>
      <svg
        ref={ref}
        className="drawing"
        viewBox="0 0 640 290"
        role="img"
        aria-label="Section view of a file split into ranges, with dimension lines and a detail of one range being halved"
      >
        <defs>
          <pattern id="hatch" width="7" height="7" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
            <line x1="0" y1="0" x2="0" y2="7" stroke="var(--line)" strokeWidth="1" opacity="0.7" />
          </pattern>
        </defs>
        <g fill="none" stroke="var(--line)" strokeWidth="1.1">
          {/* overall dimension */}
          <line className="trace" x1="20" y1="34" x2="620" y2="34" />
          <line className="trace" x1="20" y1="28" x2="20" y2="60" strokeDasharray="2 3" />
          <line className="trace" x1="620" y1="28" x2="620" y2="60" strokeDasharray="2 3" />
          <text x="320" y="26" textAnchor="middle" stroke="none">
            87 031 808 bytes, preallocated
          </text>

          {/* file body */}
          <rect className="trace" x="20" y="60" width="600" height="70" />

          {/* range boundaries, as found mid-transfer */}
          <line className="trace" x1="320" y1="60" x2="320" y2="130" />
          <line className="trace" x1="470" y1="60" x2="470" y2="130" />
          <line className="trace" x1="545" y1="60" x2="545" y2="130" />
          <line className="trace" x1="582" y1="60" x2="582" y2="130" />
          <line className="trace" x1="601" y1="60" x2="601" y2="130" />
          <line className="trace" x1="610" y1="60" x2="610" y2="130" />

          {/* written portions */}
          <rect className="fill" x="21" y="61" width="210" height="68" fill="url(#hatch)" stroke="none" />
          <rect className="fill" x="321" y="61" width="90" height="68" fill="url(#hatch)" stroke="none" />
          <rect className="fill" x="471" y="61" width="60" height="68" fill="url(#hatch)" stroke="none" />
          <rect className="fill" x="546" y="61" width="12" height="68" fill="url(#hatch)" stroke="none" />
          <rect className="fill" x="583" y="61" width="14" height="68" fill="url(#hatch)" stroke="none" />

          {/* workers */}
          <g stroke="none" className="strong">
            <text className="strong" x="170" y="100" textAnchor="middle">Connection 1</text>
            <text className="strong" x="395" y="100" textAnchor="middle">2</text>
            <text className="strong" x="507" y="100" textAnchor="middle">3</text>
            <text className="strong" x="563" y="100" textAnchor="middle" fontSize="9">4</text>
            <text className="strong" x="591" y="100" textAnchor="middle" fontSize="9">5</text>
          </g>

          {/* range dimensions */}
          <line className="trace" x1="20" y1="150" x2="620" y2="150" />
          <line className="trace" x1="320" y1="144" x2="320" y2="156" />
          <line className="trace" x1="470" y1="144" x2="470" y2="156" />
          <line className="trace" x1="545" y1="144" x2="545" y2="156" />
          <text x="170" y="167" textAnchor="middle" stroke="none">43 515 904</text>
          <text x="395" y="167" textAnchor="middle" stroke="none">21 757 952</text>
          <text x="507" y="167" textAnchor="middle" stroke="none">10 878 976</text>
          <text x="582" y="167" textAnchor="middle" stroke="none" fontSize="9">see detail</text>

          {/* detail: one range being halved */}
          <circle className="trace" cx="600" cy="95" r="26" strokeDasharray="3 3" />
          <line className="trace" x1="600" y1="121" x2="470" y2="215" strokeDasharray="3 3" />
          <circle className="trace" cx="440" cy="245" r="42" />
          <text x="440" y="192" textAnchor="middle" stroke="none">Detail, a split</text>
          <rect className="trace" x="408" y="233" width="64" height="24" />
          <rect className="fill" x="409" y="234" width="18" height="22" fill="url(#hatch)" stroke="none" />
          <line className="trace" x1="449" y1="227" x2="449" y2="263" stroke="var(--chalk)" strokeWidth="1.5" />
          <text className="chalk" x="461" y="226" fontSize="9" textAnchor="middle" stroke="none">6 takes the tail</text>
          <text x="428" y="272" fontSize="9" textAnchor="middle" stroke="none">4 keeps the head</text>

          {/* offsets */}
          <line className="trace" x1="20" y1="130" x2="20" y2="190" strokeDasharray="2 3" />
          <line className="trace" x1="620" y1="130" x2="620" y2="190" strokeDasharray="2 3" />
          <text x="20" y="203" stroke="none">offset 0</text>
          <text x="620" y="203" textAnchor="end" stroke="none">offset 87 031 807</text>

          {/* one note */}
          <text x="20" y="250" stroke="none">Hatched: written to disk. Connection 4 is a slow socket.</text>
          <text x="20" y="268" stroke="none">All writes positioned. No part files, no merge.</text>
        </g>
      </svg>
      <figcaption>
        <b>Fig. 1</b>
        Section through a download in progress. Each idle connection halves the largest
        remaining range and takes the tail, so the slow socket ends up owning a sliver.
      </figcaption>
    </figure>
  );
}
