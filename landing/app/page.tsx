import CopyCommand from "@/components/CopyCommand";

// Where the setup program lives. Point this at a release once there is one.
const setupUrl = "/DM-Setup.exe";

const downloads = [
  {
    name: "ubuntu-24.04.1-desktop-amd64.iso",
    host: "releases.ubuntu.com",
    size: "5.8 GB",
    lanes: [100, 100, 78, 62, 40, 22, 12, 6],
    status: <><b>41.4 MB/s</b> · 40%</>,
  },
  {
    name: "lecture-07-distributed-systems.mp4",
    host: "HLS, 412 of 580 segments",
    size: "1.2 GB",
    lanes: [100, 100, 100, 100, 100, 70, 0, 0],
    status: <><b>12.9 MB/s</b> · 71%</>,
  },
  {
    name: "blender-4.2-windows-x64.zip",
    host: "download.blender.org",
    size: "336 MB",
    lanes: [100, 100, 100, 100, 100, 100, 100, 100],
    status: <><b>done</b> · 8.1 s</>,
  },
];

const cells = [
  ["×8", "Eight connections, one file", "Hosts cap the socket, not you. Ranges are split as the transfer runs and every byte is written to its final offset. No part files, no merge."],
  ["↺", "Exact resume", "Finished ranges are recorded as they land. Restart the app, the PC or the network and DM continues from the byte it stopped at."],
  ["▶", "Streams to MP4", "HLS playlists are fetched segment by segment and, with ffmpeg present, written out as one MP4."],
  ["⌂", "Native window", "Dark Windows app with a tray icon, save-as and progress dialogs, and finish actions: open, open folder, sleep, shut down."],
  ["$", "Web UI and CLI", "The same list on localhost for the browser, and dm add, dm ls, dm pause for scripts."],
  ["◫", "No admin prompt", "Installs to your user folder. The same file uninstalls and keeps your download list unless told otherwise."],
];

export default function Page() {
  return (
    <>
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
              dm
            </a>
            <nav>
              <a href="#features">Features</a>
              <a href="#handoff">Extension</a>
              <a href="#install">Docs</a>
              <a className="solid" href={setupUrl}>
                Download
              </a>
            </nav>
          </div>
        </header>

        <section className="hero">
          <div className="in">
            <span className="eyebrow">
              <b>v0.1</b> Windows 10 and 11, no admin rights
            </span>
            <h1>The download manager for Windows.</h1>
            <p className="lede">
              Eight connections per file, exact resume after anything, streaming video to MP4,
              and a browser extension that hands links over with your cookies. One 28 MB file
              installs it.
            </p>
            <CopyCommand command="DM-Setup.exe -silent" />
            <div className="acts">
              <a className="btn" href={setupUrl}>
                Download DM-Setup.exe
              </a>
              <a className="btn ghost" href="#features">
                How it works
              </a>
            </div>

            <div className="win" role="img" aria-label="DM download list">
              <div className="bar">
                <span>DM</span>
                <span>— ▢ ✕</span>
              </div>
              {downloads.map((d) => (
                <div className="row" key={d.name}>
                  <div className="n">
                    {d.name}
                    <small>{d.host}</small>
                  </div>
                  <div className="s">{d.size}</div>
                  <div className="bar8">
                    {d.lanes.map((p, i) => (
                      <i key={i} style={{ "--p": `${p}%` } as React.CSSProperties} />
                    ))}
                  </div>
                  <div className="t">{d.status}</div>
                </div>
              ))}
            </div>
          </div>
        </section>

        <section className="nums" aria-label="Numbers">
          <div className="in">
            <div>
              <b>8</b>
              <span>connections per file</span>
            </div>
            <div>
              <b>6.5 s</b>
              <span>for 83 MiB, browser took 13.7 s</span>
            </div>
            <div>
              <b>9 MB</b>
              <span>idle memory</span>
            </div>
            <div>
              <b>0</b>
              <span>third-party modules</span>
            </div>
          </div>
        </section>

        <section className="cells" id="features">
          <div className="in">
            {cells.map(([ic, h, p]) => (
              <div className="cell" key={h}>
                <div className="ic">{ic}</div>
                <h3>{h}</h3>
                <p>{p}</p>
              </div>
            ))}
          </div>
        </section>

        <section className="log" id="handoff">
          <div className="in">
            <div>
              <h2>The extension hands over the link.</h2>
              <p>
                Chrome, Edge, Brave and Vivaldi. When the browser starts a download the extension
                intercepts it and passes URL and cookies to DM over native messaging. Files behind
                a login download the way they would in the browser, only faster. Video pages get a
                button.
              </p>
            </div>
            <div>
              <pre>
                <i>12:04:01.203</i> <b>ext</b>   download intercepted  ubuntu-24.04.1-desktop-amd64.iso{"\n"}
                <i>12:04:01.204</i> <b>ext</b>   cookies attached      2{"\n"}
                <i>12:04:01.211</i> <b>dm</b>    HEAD ok  5.8 GB  ranges: yes{"\n"}
                <i>12:04:01.212</i> <b>dm</b>    preallocated, 8 connections{"\n"}
                <i>12:04:01.240</i> <b>dm</b>    c1 200  0-3 116 000 000{"\n"}
                <i>12:04:01.241</i> <b>dm</b>    c2 206  3 116 000 000-5 800 000 000{"\n"}
                <i>12:04:03.902</i> <b>dm</b>    c4 idle, split c1: takes 1 558 000 000-{"\n"}
                <i>12:04:07.115</i> <b>dm</b>    41.4 MB/s  40%
              </pre>
            </div>
          </div>
        </section>

        <footer className="foot" id="install">
          <div className="in">
            <span>DM, a download manager for Windows. Written in Go.</span>
            <span>
              <a href={setupUrl}>Download</a>
              <a href="#">Changelog</a>
              <a href="#">Source</a>
            </span>
          </div>
        </footer>
      </div>
    </>
  );
}
