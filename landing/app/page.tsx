import FileSection from "@/components/FileSection";
import Measurement from "@/components/Measurement";
import Handoff from "@/components/Handoff";

// Where the setup program lives. Point this at a release once there is one.
const setupUrl = "/DM-Setup.exe";

export default function Page() {
  return (
    <div className="sheet">
      <header className="top">
        <span className="label">DM, download manager for Windows</span>
        <nav>
          <a href="#figures">Figures</a>
          <a href="#notes">Notes</a>
          <a href="#install">Install</a>
        </nav>
      </header>

      <section className="hero">
        <div>
          <h1 className="display">One file, eight connections, drawn to scale</h1>
          <p>
            DM downloads a file over eight connections, splits the remaining bytes as it goes
            and writes every piece to its final offset. The browser extension hands over the
            link with your cookies. Windows, no admin rights.
          </p>
          <a className="button solid" href={setupUrl}>
            Download DM-Setup.exe
          </a>
          <a className="button" href="#figures">
            Read the drawing
          </a>
          <small>28 MB. The same file uninstalls.</small>
        </div>
        <FileSection />
      </section>

      <div className="figs" id="figures">
        <section>
          <h2 className="display">The measurement</h2>
          <p>
            An 83 MiB file from dl.google.com, one connection against eight, same machine,
            byte-identical output.
          </p>
          <Measurement />
        </section>
        <section>
          <h2 className="display">The hand-off</h2>
          <p>
            The browser starts a download; the extension intercepts it and passes link and
            cookies to DM over the browser&apos;s native-messaging channel.
          </p>
          <Handoff />
        </section>
      </div>

      <div className="notes" id="notes">
        <div>
          <h2 className="display">General notes</h2>
          <ol>
            <li>
              <b>Resume is exact.</b> Finished ranges are recorded as they land. After a
              restart of the app, the PC or the network, transfer continues from the byte it
              stopped at.
            </li>
            <li>
              <b>Streaming video becomes one file.</b> HLS playlists are fetched segment by
              segment and, with ffmpeg present, written out as MP4.
            </li>
            <li>
              <b>The window is native.</b> Dark Windows app with a tray icon, save-as and
              progress dialogs, a finished dialog, and finish actions: open the file, open the
              folder, sleep, shut down, restart.
            </li>
            <li>
              <b>Other ways in.</b> A web page on localhost shows the same list. A command line
              adds, lists, pauses and resumes for scripts.
            </li>
            <li>
              <b>Nothing to clean up.</b> The installer needs no administrator prompt and the
              uninstaller keeps your download list unless told otherwise.
            </li>
          </ol>
        </div>
        <div>
          <h2 className="display">Materials</h2>
          <table className="spec">
            <tbody>
              <tr>
                <th>Language</th>
                <td>Go, no third-party modules</td>
              </tr>
              <tr>
                <th>Idle memory</th>
                <td>about 9 MB</td>
              </tr>
              <tr>
                <th>Connections per file</th>
                <td>8, split dynamically</td>
              </tr>
              <tr>
                <th>Install location</th>
                <td>your user folder</td>
              </tr>
              <tr>
                <th>Setup size</th>
                <td>28 MB</td>
              </tr>
              <tr>
                <th>Optional</th>
                <td>ffmpeg for MP4 output</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <section className="get" id="install">
        <div>
          <h2 className="display">Install from one file</h2>
          <p>
            DM-Setup.exe installs the app, registers the browser hook and offers the extension.
            Unattended: <code>DM-Setup.exe -silent</code>.
          </p>
        </div>
        <a className="button solid" href={setupUrl}>
          Download DM-Setup.exe
        </a>
      </section>

      <footer className="title-block">
        <div>
          <span className="label">Title</span>
          <span className="v big">DM</span>
          <span className="v">Download manager for Windows</span>
        </div>
        <div>
          <span className="label">Drawn in</span>
          <span className="v">Go</span>
        </div>
        <div>
          <span className="label">Revision</span>
          <span className="v">0.1.0</span>
        </div>
        <div>
          <span className="label">Sheet</span>
          <span className="v">1 of 1</span>
        </div>
      </footer>
    </div>
  );
}
