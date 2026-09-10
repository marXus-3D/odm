import type { NextConfig } from "next";

// The landing page is a static site: `next build` writes plain HTML, CSS and
// JS to ./out, which any file host can serve.
const nextConfig: NextConfig = {
  output: "export",
  images: { unoptimized: true },
};

export default nextConfig;
