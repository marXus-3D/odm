import type { CSSProperties } from "react";
import CopyCommand from "@/components/CopyCommand";
import Motion from "@/components/Motion";

// The repository and the newest release's setup program. GitHub redirects
// the second URL to the asset of the latest published release.
const repo = "https://github.com/marXus-3D/odm";
const setupUrl = `${repo}/releases/latest/download/ODM-Setup.exe`;

const downloads = [
  {
    name: "ubuntu-24.04.1-desktop-amd64.iso",
    host: "releases.ubuntu.com",
    size: "5.8 GB",
    lanes: [100, 100, 78, 62, 40, 22, 12, 6],
    live: true,
    status: (
      <>
        <b>41.4 MB/s</b> · 40%
      </>
    ),
  },
  {
    name: "lecture-07-distributed-systems.mp4",
    host: "HLS, 412 of 580 segments",
    size: "1.2 GB",
    lanes: [100, 100, 100, 100, 100, 70, 0, 0],
    live: true,
    status: (
      <>
        <b>12.9 MB/s</b> · 71%
      </>
    ),
  },
  {
    name: "blender-4.2-windows-x64.zip",
    host: "download.blender.org",
    size: "336 MB",
    lanes: [100, 100, 100, 100, 100, 100, 100, 100],
    live: false,
    status: (
      <>
        <b>done</b> · 8.1 s
      </>
    ),
  },
];

const cells: [string, string, string][] = [
  [
    "×8",
    "Eight connections, one file",
    "Hosts cap the socket, not you. Ranges are split as the transfer runs and every byte is written to its final offset. No part files, no merge.",
  ],
  [
    "↺",
    "Exact resume",
    "Finished ranges are recorded as they land. Restart the app, the PC or the network and ODM continues from the byte it stopped at.",
  ],
  [
    "▶",
    "Streams to MP4",
    "HLS playlists are fetched segment by segment and, with ffmpeg present, written out as one MP4.",
  ],
  [
    "⌂",
    "Native window",
    "Dark Windows app with a tray icon, save-as and progress dialogs, and finish actions: open, open folder, sleep, shut down.",
  ],
  [
    "$",
    "Web UI and CLI",
    "The same list on localhost for the browser, and odm add, odm ls, odm pause for scripts.",
  ],
  [
    "◫",
    "No admin prompt",
    "Installs to your user folder. The same file uninstalls and keeps your download list unless told otherwise.",
  ],
];

const faq: [string, string][] = [
  [
    "Does it need administrator rights?",
    "No. ODM installs to your user folder and registers the browser hook for your user only, so Windows never shows an elevation prompt. For unattended installs run ODM-Setup.exe -silent.",
  ],
  [
    "What happens if my PC restarts mid-download?",
    "Nothing is lost. Every finished range is on disk at its final offset and recorded the moment it lands. The next time ODM runs, the transfer continues from the byte it stopped at.",
  ],
  [
    "Which browsers does the extension support?",
    "Chrome, Edge, Brave and Vivaldi, over the browser's native-messaging channel. From any other browser, paste the link into ODM or use the command line.",
  ],
  [
    "Do downloads behind a login work?",
    "Yes. The extension hands ODM the link together with the cookies the browser would have sent, so files behind a sign-in download the way they would in the browser, only faster.",
  ],
  [
    "What about streaming video?",
    "Point ODM at an HLS playlist, or press the button the extension adds on video pages. Segments come down in parallel and, with ffmpeg installed, are written out as a single MP4.",
  ],
  [
    "Is eight connections always faster?",
    "No. It helps most on hosts that cap each socket and on busy links, which is most of them. A host that caps per user, or a line that is already saturated, shows a smaller gap. The 83 MiB test on this page had no server-side limit.",
  ],
  [
    "Is it open source?",
    "Yes, under the MIT license. The engine, the window, the extension and the setup program are all in one repository on GitHub, and releases are built there by a workflow from a tagged commit, so the ODM-Setup.exe you download is the one the public build produced.",
  ],
  [
    "How do I uninstall?",
    "Run the same ODM-Setup.exe again. It removes the app and the browser hook and keeps your download list unless you tell it not to.",
  ],
];

export default function Page() {
  return (
    <>
      <Motion />
      <div className="light" aria-hidden="true">
        <div className="core" />
        <div className="cone" />
        <div className="rays soft" />
        <div className="rays" />
        <div className="dust" />
      </div>

      <div className="page">
        <header className="top">
          <div className="in">
            <a className="brand" href="#">
              <i />
              odm
            </a>
            <nav>
              <a href="#how">How it works</a>
              <a href="#features">Features</a>
              <a href="#extension">Extension</a>
              <a href="#faq">FAQ</a>
              <a href={repo}>GitHub</a>
              <a className="solid" href={setupUrl}>
                Download
              </a>
            </nav>
          </div>
        </header>

        {/* hero */}
        <section className="hero">
          <div className="in">
            <div data-hero>
              <span className="eyebrow">
                <b>v0.1</b> Open source · Windows 10 and 11 · no admin rights
              </span>
              <h1>The open download manager for Windows.</h1>
              <p className="lede">
                Eight connections per file, exact resume after anything,
                streaming video to MP4, and a browser extension that hands links
                over with your cookies. One 28 MB file installs it.
              </p>
              <CopyCommand command="ODM-Setup.exe -silent" />
              <div className="acts">
                <a className="btn" href={setupUrl}>
                  Download ODM-Setup.exe
                </a>
                <a className="btn ghost" href="#how">
                  How it works
                </a>
              </div>
            </div>

            <div className="win" role="img" aria-label="ODM download list">
              <div className="bar">
                <span>ODM</span>
                <span>— ▢ ✕</span>
              </div>
              {downloads.map((d) => (
                <div
                  className="row"
                  key={d.name}
                  data-live={d.live ? "" : undefined}
                >
                  <div className="n">
                    {d.name}
                    <small>{d.host}</small>
                  </div>
                  <div className="s">{d.size}</div>
                  <div className="bar8">
                    {d.lanes.map((p, i) => (
                      <i key={i}>
                        <span style={{ width: `${p}%` }} />
                      </i>
                    ))}
                  </div>
                  <div className="t">{d.status}</div>
                </div>
              ))}
            </div>
          </div>
        </section>

        {/* numbers */}
        <section className="nums" aria-label="Numbers">
          <div className="in">
            <div>
              <b data-count="8">8</b>
              <span>connections per file</span>
            </div>
            <div>
              <b data-count="6.5" data-decimals="1" data-suffix=" s">
                6.5 s
              </b>
              <span>for 83 MiB, browser took 13.7 s</span>
            </div>
            <div>
              <b data-count="9" data-suffix=" MB">
                9 MB
              </b>
              <span>idle memory</span>
            </div>
            <div>
              <b data-count="0">0</b>
              <span>third-party modules</span>
            </div>
          </div>
        </section>

        {/* how it works */}
        <section className="sec how" id="how">
          <div className="in">
            <div className="head" data-reveal data-stagger>
              <div>
                <span className="label mono">01 · How it works</span>
                <h2>One file, eight ranges, written in place.</h2>
              </div>
              <p>
                ODM asks the server for the size, preallocates the file and
                splits it into ranges. Every connection writes to its own offset
                in the same file, so there is nothing to merge when it finishes,
                and a slow connection never sets the finishing time.
              </p>
            </div>

            <div className="split" data-split>
              <div>
                <div
                  className="track"
                  aria-label="A file split into eight ranges, one being halved"
                >
                  {[0, 12.5, 25, 37.5, 50, 62.5, 75, 87.5].map((left, i) => (
                    <div
                      className={`range${i === 3 ? " slow" : ""}`}
                      key={i}
                      style={
                        {
                          left: `${left}%`,
                          width: i === 3 ? "7.5%" : "12.5%",
                        } as CSSProperties
                      }
                    >
                      <span
                        className="fill"
                        style={{ width: i === 3 ? "60%" : "100%" }}
                      />
                      <em>c{i + 1}</em>
                    </div>
                  ))}
                  <div
                    className="range new"
                    style={{ left: "45%", width: "5%" }}
                  >
                    <span className="fill" style={{ width: "100%" }} />
                    <em>c1</em>
                  </div>
                </div>
                <div className="axis mono">
                  <span>offset 0</span>
                  <span>87 031 807</span>
                </div>
              </div>
              <ol className="steps">
                <li data-step="0">
                  <b>Size and preallocate.</b> One request finds the length. The
                  file is created at full size so every write already has its
                  place.
                </li>
                <li data-step="1">
                  <b>Eight ranges, eight sockets.</b> Each connection asks for
                  its byte range and writes at that offset. No part files.
                </li>
                <li data-step="2" className="on">
                  <b>Idle hands take the tail.</b> c4 is a slow socket. When c1
                  finishes it halves what c4 has left and downloads the second
                  half, so the slow one ends with a sliver.
                </li>
              </ol>
            </div>
          </div>
        </section>

        {/* features */}
        <section className="cells" id="features">
          <div className="in" data-reveal data-stagger>
            {cells.map(([ic, h, p]) => (
              <div className="cell" key={h}>
                <div className="ic">{ic}</div>
                <h3>{h}</h3>
                <p>{p}</p>
              </div>
            ))}
          </div>
        </section>

        {/* measured */}
        <section className="sec speed" id="speed">
          <div className="in">
            <div className="head" data-reveal data-stagger>
              <div>
                <span className="label mono">02 · Measured</span>
                <h2>13.7 seconds becomes 6.5.</h2>
              </div>
              <p>
                An 83 MiB file from dl.google.com with no server-side rate
                limit, one connection against eight, same machine,
                byte-identical output. Drawn to scale below.
              </p>
            </div>

            <div className="race" data-race>
              <div className="lane" data-seconds="13.7">
                <span className="who">
                  1 connection <small>the browser</small>
                </span>
                <div className="bar">
                  <span className="fill" style={{ width: "100%" }} />
                </div>
                <span className="time mono" data-time>
                  13.7 s
                </span>
              </div>
              <div className="lane odm" data-seconds="6.5">
                <span className="who">
                  8 connections <small>ODM</small>
                </span>
                <div className="bar">
                  <span className="fill" style={{ width: "47.4%" }} />
                </div>
                <span className="time mono" data-time>
                  6.5 s
                </span>
              </div>
            </div>

            <div className="why" data-reveal data-stagger>
              <div>
                <h3>Hosts cap the socket.</h3>
                <p>
                  Most servers throttle each connection rather than each person.
                  Eight connections get eight times the cap.
                </p>
              </div>
              <div>
                <h3>Links are shared per flow.</h3>
                <p>
                  TCP divides a busy bottleneck between flows, not users. Eight
                  flows claim eight shares and each ramps up on its own.
                </p>
              </div>
              <div>
                <h3>No slow socket sets the finish.</h3>
                <p>
                  An idle connection halves the largest remaining range and
                  takes the tail, so a slow one keeps losing ground until the
                  fast ones are done.
                </p>
              </div>
            </div>
          </div>
        </section>

        {/* four ways in */}
        <section className="sec ways" id="ways">
          <div className="in">
            <div className="head" data-reveal data-stagger>
              <div>
                <span className="label mono">03 · Four ways in</span>
                <h2>One list underneath.</h2>
              </div>
              <p>
                Window, web page, command line and browser extension all talk to
                the same ODM. Add a download in one place and it shows up in the
                others.
              </p>
            </div>
            <div className="quad" data-reveal data-stagger>
              <div>
                <div className="mock mock-win">
                  <div className="tb">
                    <span>ODM</span>
                    <span>— ▢ ✕</span>
                  </div>
                  <i style={{ width: "40%" }} />
                  <i style={{ width: "71%" }} />
                  <i style={{ width: "100%" }} />
                </div>
                <h3>Native window</h3>
                <p>
                  Dark, with a tray icon, save-as and progress dialogs, and
                  finish actions from open folder to shut down.
                </p>
              </div>
              <div>
                <div className="mock mock-web">
                  <div className="url mono">localhost:7780</div>
                  <i style={{ width: "40%" }} />
                  <i style={{ width: "71%" }} />
                </div>
                <h3>Web page</h3>
                <p>
                  The same list in any browser on this machine. Useful on a
                  headless box or over remote desktop.
                </p>
              </div>
              <div>
                <div className="mock mock-cli mono">
                  <span>&gt; odm add https://…/x.iso</span>
                  <span className="dim">a91f added, 8 connections</span>
                  <span>&gt; odm ls</span>
                  <span className="dim">a91f 40% 41.4 MB/s</span>
                </div>
                <h3>Command line</h3>
                <p>
                  add, ls, pause, resume and rm for scripts and scheduled tasks.
                  Output is plain text, one line per download.
                </p>
              </div>
              <div>
                <div className="mock mock-ext">
                  <div className="url mono">example.com/files/report.pdf</div>
                  <span className="chip">Download with ODM</span>
                </div>
                <h3>Browser extension</h3>
                <p>
                  Intercepts downloads in Chrome, Edge, Brave and Vivaldi and
                  hands them over with your cookies. Video pages get a button.
                </p>
              </div>
            </div>
          </div>
        </section>

        {/* extension */}
        <section className="log" id="extension">
          <div className="in">
            <div data-reveal>
              <span className="label mono">04 · The hand-off</span>
              <h2>The extension hands over the link.</h2>
              <p>
                When the browser starts a download the extension intercepts it
                and passes URL and cookies to ODM over native messaging. No
                network port is opened. Files behind a login download the way
                they would in the browser, only faster.
              </p>
              <div className="browsers">
                <span>Chrome</span>
                <span>Edge</span>
                <span>Brave</span>
                <span>Vivaldi</span>
              </div>
            </div>
            <div data-log>
              <pre>
                {[
                  [
                    "12:04:01.203",
                    "ext",
                    "download intercepted  ubuntu-24.04.1-desktop-amd64.iso",
                  ],
                  ["12:04:01.204", "ext", "cookies attached      2"],
                  ["12:04:01.211", "odm", "HEAD ok  5.8 GB  ranges: yes"],
                  ["12:04:01.212", "odm", "preallocated, 8 connections"],
                  ["12:04:01.240", "odm", "c1 200  0-3 116 000 000"],
                  [
                    "12:04:01.241",
                    "odm",
                    "c2 206  3 116 000 000-5 800 000 000",
                  ],
                  [
                    "12:04:03.902",
                    "odm",
                    "c4 idle, split c1: takes 1 558 000 000-",
                  ],
                  ["12:04:07.115", "odm", "41.4 MB/s  40%"],
                ].map(([t, who, msg]) => (
                  <span data-line key={t}>
                    <i>{t}</i> <b>{who}</b> {msg}
                    {"\n"}
                  </span>
                ))}
              </pre>
            </div>
          </div>
        </section>

        {/* faq */}
        <section className="sec faq" id="faq">
          <div className="in">
            <div className="head" data-reveal data-stagger>
              <div>
                <span className="label mono">05 · Questions</span>
                <h2>Short answers.</h2>
              </div>
              <p>
                Anything not here is in the <a href={`${repo}#readme`}>README</a>, or open
                an <a href={`${repo}/issues`}>issue</a>.
              </p>
            </div>
            <div className="qa" data-reveal data-stagger>
              {faq.map(([q, a]) => (
                <details key={q}>
                  <summary>
                    {q}
                    <i aria-hidden="true" />
                  </summary>
                  <p>{a}</p>
                </details>
              ))}
            </div>
          </div>
        </section>

        {/* install */}
        <section className="install" id="install">
          <div className="in" data-reveal data-stagger>
            <span className="label mono">06 · Install</span>
            <h2>One file. No prompt.</h2>
            <p>
              ODM-Setup.exe installs to your user folder, registers the browser
              hook and offers the extension. The same file uninstalls.
            </p>
            <div className="acts">
              <a className="btn" href={setupUrl}>
                Download ODM-Setup.exe
              </a>
              <CopyCommand command="ODM-Setup.exe -silent" />
            </div>
            <small>
              Windows 10 and 11 · 28 MB · open source, MIT · written in Go with
              no third-party modules · 9 MB idle
            </small>
          </div>
        </section>

        <footer className="foot">
          <div className="in">
            <div className="cols">
              <div>
                <a className="brand" href="#">
                  <i />
                  odm
                </a>
                <p>A download manager for Windows.</p>
              </div>
              <div>
                <h4>Product</h4>
                <a href={setupUrl}>Download</a>
                <a href="#how">How it works</a>
                <a href="#faq">FAQ</a>
              </div>
              <div>
                <h4>Docs</h4>
                <a href={`${repo}#readme`}>Getting started</a>
                <a href={`${repo}#cli`}>Command line</a>
                <a href={`${repo}#install-it`}>Extension</a>
              </div>
              <div>
                <h4>Project</h4>
                <a href={repo}>Source</a>
                <a href={`${repo}/releases`}>Releases</a>
                <a href={`${repo}/issues`}>Issues</a>
              </div>
            </div>
            <div className="fine">
              <span>ODM 0.1 · MIT</span>
              <span>Windows 10 and 11</span>
            </div>
          </div>
        </footer>
      </div>
    </>
  );
}
