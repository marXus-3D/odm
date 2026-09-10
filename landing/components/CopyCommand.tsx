"use client";

import { useState } from "react";

// The install command with a copy button. The blinking block is the only
// thing on the page that moves besides the light.
export default function CopyCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      setTimeout(() => setCopied(false), 1600);
    } catch {
      // Clipboard denied. The text is selectable; nothing else to do.
    }
  }

  return (
    <div className="cmd mono">
      <span>
        <span className="p">&gt;</span> {command}
        <span className="cursor" aria-hidden="true" />
      </span>
      <button type="button" onClick={copy} aria-label="Copy the install command">
        {copied ? "copied" : "copy"}
      </button>
    </div>
  );
}
