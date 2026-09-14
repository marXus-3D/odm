# ODM, Open Download Manager

An IDM-style download manager for Windows: a native desktop app with
parallel range downloads, dynamic segmentation, resume across restarts, HLS
video, a tray icon, a web UI, and a Chrome extension that hands the
browser's downloads over — cookies and all.

Written in Go with no third-party modules. The idle daemon sits at about
9 MB of RSS. `ffmpeg` is used if present, to convert downloaded HLS video to
MP4, and nothing breaks without it.

## Why it is fast

Three things, in order of how much they actually matter:

1. **Per-connection throttling.** Most hosts cap each socket, not each IP.
   Eight sockets get eight times the cap.
2. **TCP congestion control is per-flow.** On a shared bottleneck, eight flows
   claim eight shares instead of one, and each ramps its window independently
   — which matters most on high latency-bandwidth paths.
3. **Dynamic segmentation.** See below.

Measured on this machine against `dl.google.com` (83 MiB, no server-side rate
limit): **13.7s at 1 connection → 6.5s at 8**, byte-identical output.

### Dynamic segmentation

The naive approach splits the file into N fixed chunks up front. Every fast
connection then sits idle while one straggler grinds through its share, and
the slowest peer sets the total time.

ODM instead starts with a single whole-file range. Each idle worker splits the
largest *remaining* range in half and takes the tail
([`acquire`](internal/engine/download.go)). Workers stay busy to the end of
the transfer, and a slow peer automatically ends up owning less of the file.
The same code path handles both the initial fan-out and late-transfer
rebalancing.

Segments are written straight into one preallocated file with `WriteAt` at
their own offsets — no temp part files and no merge pass at the end.

### When it backs off

A 429 or 503 means the server wants *fewer* connections, so retrying harder is
the wrong move. Every worker but the last hands its range back and exits; the
survivor waits out `Retry-After`. Hetzner's speed-test host rate-limits
exactly this way, and ODM finishes the transfer instead of failing.

A connection that stops delivering bytes for 30s is dropped and its range
retried on a fresh socket, so a black-holed route cannot pin a worker.

## Layout

```
cmd/odm         CLI: standalone downloads, and a client for the daemon
cmd/odmd        the app: desktop window, tray, queue, web UI, HTTP API
cmd/odm-nmh     Chrome native messaging host
cmd/odm-setup   installs the browser integration
cmd/odm-installer  the setup program: ODM-Setup.exe, and the uninstaller
cmd/odm-pack    build tool: signs the extension into a .crx
internal/engine   byte-range segments, dynamic splitting, resume, retry
internal/hls      M3U8 parsing, parallel segment fetch, AES-128, remux
internal/manager  queue, concurrency limits, engine selection
internal/api      HTTP API + embedded web UI
internal/nativeui the Win32 desktop window
internal/trayicon notification-area icon
internal/client   shared daemon client (CLI, window and native host)
internal/store    persisted download list and config
internal/crx      CRX3 packer, so the installer can ship a signed extension
internal/shortcut .lnk files through IShellLink
internal/startup  the HKCU run key
extension/        MV3 Chrome extension
```

## Build

```powershell
.\build.ps1
```

That builds the four binaries into `.\bin`, runs the tests, validates the
extension and packages it to `.\dist\odm-extension-<version>.zip`. Use
`-SkipTests` or `-SkipExtension` to do less.

Go 1.22+ is required (the API uses method-aware `http.ServeMux` patterns).
`ffmpeg` is optional; see "TS to MP4".

## Install it

```
dist\ODM-Setup.exe
```

One file. It carries the binaries, the extension and the signed `.crx`
inside itself and installs per user, so there is no administrator prompt:

- program files in `%LOCALAPPDATA%\Programs\ODM`
- a Start menu shortcut, and a desktop one if asked
- start at sign-in, through the `HKCU` run key
- an **Add or remove programs** entry, which runs the same exe with
  `-uninstall`
- the native messaging host, and the extension offered to every installed
  Chromium browser

The extension is the one part nobody can fully automate: Chrome will not
silently enable an extension that did not come from the Web Store. The
installer writes the external-extension registry entry, which is the
supported way to offer one, and the finish page also has **Load the
extension**, which opens `chrome://extensions` with the folder already on
the clipboard for **Load unpacked**.

For a scripted install:

```
dist\ODM-Setup.exe -silent [-dir PATH] [-no-startup] [-no-desktop] [-no-browser]
dist\ODM-Setup.exe -uninstall -silent [-remove-data]
```

Uninstalling keeps the download list and settings in `%APPDATA%\odm` unless
`-remove-data` says otherwise.

## Use it

### CLI

Download right now, in this process:

```bash
./bin/odm.exe -n 8 https://example.com/big.iso
```

Ctrl-C pauses and writes resume state; rerun the same command to continue.
Flags: `-n` connections, `-d` directory, `-o` filename, `-limit` KiB/s,
`-referer`, `-cookie`, `-ua`, `-q`.

Or drive the daemon, which starts on demand:

```bash
./bin/odm.exe add https://example.com/big.iso
./bin/odm.exe ls
./bin/odm.exe pause <id>
./bin/odm.exe resume <id>
./bin/odm.exe rm -f <id>
./bin/odm.exe pause-all
./bin/odm.exe resume-all
./bin/odm.exe stop-all         # pause everything and clear the queue
./bin/odm.exe startup on       # run ODM at login
./bin/odm.exe on-finish sleep  # what to do once everything is done
./bin/odm.exe limit 500        # KiB/s across everything; "off" to remove
./bin/odm.exe open <id>        # or "show" to reveal in Explorer
./bin/odm.exe ui               # print the web UI url
./bin/odm.exe daemon stop      # graceful: pauses downloads, saves state
```

### The app

Double-click `bin\odmd.exe`, or run it:

```bash
./bin/odmd.exe
```

That opens the desktop window: a real Win32 application with a menu bar, a
toolbar, and a list showing name, size, progress, speed, status and time
left. Progress is a drawn bar coloured by state, not a number. Double click
opens a finished download and pauses a running one, right click gives the
same actions as the menus, and several rows can be selected at once.

Closing the window leaves the daemon running and the icon in the
notification area, the way a download manager should behave. **File → Exit**
or the tray's **Quit ODM** stops it properly. The tray's **Open ODM** brings
the window back; left-clicking the tray icon does the same.

> On Windows 11 new tray icons start hidden. If you cannot see it, click the
> `^` next to the clock, or turn it on under Settings → Personalisation →
> Taskbar → Other system tray icons.

There is no separate GUI binary and no dependency behind this: the window is
Win32 through `syscall`, for the same reason as the tray, so `odmd.exe` is
still one self-contained executable.

`-no-window` runs it headless, `-open` uses the browser UI instead.

**Run at login.** Options -> Start ODM with Windows, or `odm startup on`. The
setting reports the real registry state, so an entry removed behind ODM's
back is shown accurately rather than assumed.

**Categories.** Finished downloads are filed by type into General,
Compressed, Documents, Music, Programs and Video folders under the download
directory. The category is guessed from the file type and can be changed per
download.

### Dialogs

Two dialogs from IDM, both switchable from the Options menu:

- **Download File Info**, before a download starts: the URL, its category, a
  Save As path with the standard Windows browser, and a description, with
  Start Download / Download Later / Cancel. Changing the category repoints
  Save As at that category's folder, and "remember this path" makes it the
  category default. Off by default; turn it on with Options -> Ask where to
  save each download.
- **Download complete**, when one finishes: how much came down, the address,
  where it went, and Open / Open with... / Open folder / Close, plus "don't
  show this dialog again". On by default.

Downloads added by the browser extension go through the first dialog too.

If no browser has ever connected, the app says so once and offers to open
`chrome://extensions` and the folder to load.

### When everything finishes

Options -> When everything finishes, or:

```bash
./bin/odm.exe on-finish sleep      # none exit sleep hibernate shutdown restart
./bin/odm.exe on-finish cancel     # call off a pending one
```

It fires once nothing is left running, queued or remuxing, and is one-shot:
the setting clears before the action runs, so the machine does not shut down
every time the list happens to empty. Shutdown and restart use the OS timer
with a 60 second grace, so Windows shows its own countdown and `shutdown /a`
calls it off even if ODM has exited.

### Web UI

The web UI is still there and does everything the window does, plus
settings. **File → Open web UI**, the tray menu, or:

```bash
./bin/odm.exe ui
```

It shows live speed, connection count and a per-segment progress map, and
supports pause, resume, remove, open and show-in-folder.

The daemon also starts on demand — the extension launches it if it is not
already running, so you do not have to keep it running yourself. Started
that way it does not open a browser tab.

`odm.exe` is the command line client, not the app — double-clicking it just
prints its usage and exits.

`odmd` is a GUI binary, so it does not flash a console window, but it still
prints normally when run from a terminal, and always logs to
`%APPDATA%\odm\odmd.log`.

### Browser extension

```powershell
.\bin\odm-setup.exe
```

That writes the native messaging host manifest, generating a keypair for a
stable id if the manifest does not already carry one (a packaged manifest
does, and its key is left alone because the shipped `.crx` is signed with
it), and registers it for Chrome, Edge, Brave, Chromium,
Vivaldi and Opera. Then:

1. Open `chrome://extensions`
2. Enable **Developer mode**
3. **Load unpacked** → the `extension` folder
4. Check the id matches the one `odm-setup` printed

Reload the extension after any rebuild that changes it.

Undo with `.\bin\odm-setup.exe -uninstall` plus removing the extension.

**If you get "Access to the specified native messaging host is forbidden"**,
the extension's id and the id in the host manifest have diverged. Rerun
`odm-setup`, then press Reload on the extension card.

```powershell
.\bin\odm-setup.exe -check
```

prints the id in the extension manifest, the ids the host manifest allows,
the id each browser actually loaded it under, and the registry entries, and
exits non-zero naming the one that is wrong.

The id comes from the `key` in the extension manifest, so it survives moving
the folder — and `odm-setup` reads the id back out of that manifest after
writing it, rather than deriving it from the key file, because Chrome only
ever sees the manifest. It also allows the ids browsers report for this
extension and the path-derived ids Chrome uses when a manifest has no key,
so an extension loaded before the key was added keeps working.

**What the extension does.** It watches `chrome.downloads.onCreated`, cancels
anything matching your rules, and forwards the URL to the daemon along with
the tab's cookies (including HttpOnly ones), `Referer` and `User-Agent`.
Without those, an outside process fetching a logged-in download just gets a
403. There is also a "Download with ODM" context-menu item for links, images,
video and audio.

**The video panel.** A floating "Download this video" button appears over
videos, the way IDM does it. It has to work this way because a streaming
page hands the browser a `blob:` URL backed by Media Source Extensions:
there is no file to right-click and nothing for the downloads API to
intercept. The extension watches for the `.m3u8` the player fetches, and the
button uses that. A video that does have a real source uses its own URL
instead. The same finds are counted on the toolbar badge and listed in the
popup. See "Streaming video" below.

The panel lives in a closed shadow root so page CSS cannot break it, is
injected into iframes since that is where players usually live, ignores
anything under 120px, and can be dismissed per video with the x.

Settings (extension options page): on/off, minimum size, whether to grab
unknown-size downloads, file types to ignore, notifications.

If the daemon cannot be reached the extension says so and lets Chrome do the
download normally, rather than losing it.

## Queues

Every download waits in a queue, and each queue decides how many of its
downloads run at once. One queue for a game, crawling along on a single
connection, and another for videos three at a time:

```
odm queues                       list them, with what is running and waiting
odm queue add -n 1 Games         create one
odm queue set -n 3 Games         change how many run at once
odm queue rm Games               delete it; its downloads move to the default
odm add -q Videos <url>          add straight into a queue
odm move <id>... Games           send existing downloads to another queue
```

In the app the same lives under **Download queues...** in the menu, the
Download File Info form has a **Queue** picker, and the right-click menu on
the list has **Move to queue**. A queue that is full does not hold up the
others: the scheduler skips past its downloads and starts the next one
whose own queue has room.

The default queue, **Main**, cannot be removed; it catches downloads that
arrive from the browser without a queue in mind.

## Streaming video

Give ODM an `.m3u8` URL -- from the extension popup, the web UI or
`odm add` -- and it is recognised automatically and fetched as a playlist
rather than saved as a text manifest.

- Segments are downloaded many at a time and written strictly in playlist
  order, with a bounded lookahead so memory stays flat on a long video.
- AES-128 encrypted playlists are decrypted, including the case where
  EXT-X-KEY carries no IV and the spec derives one from the segment's media
  sequence number.
- fMP4 playlists work, including the layout where every segment, the
  initialization segment included, is a byte range of a single container.
- Resume works at segment granularity; the file is truncated back to the
  last complete segment first, since a half-written segment would corrupt
  the join.
- Live streams (no `EXT-X-ENDLIST`) are refused up front -- there is no
  "whole file" to download.
- A finished `.ts` is remuxed to `.mp4` when `ffmpeg` is on `PATH`. It is a
  stream copy, not a transcode, with `+faststart` so the file streams. See
  below.

Verified against two real streams: a 64-segment MPEG-TS (119245 packets,
zero bad sync bytes) and Apple's 100-segment byte-range fMP4 example (602
ISO-BMFF boxes ending exactly at EOF).

DASH (`.mpd`) is detected and refused rather than silently saving the
manifest.

### TS to MP4

HLS segments concatenate into a valid `.ts`, but plenty of players and
editors will not open one, so ODM converts it.

```powershell
winget install --id Gyan.FFmpeg -e
```

Restart the daemon afterwards so it inherits the new `PATH`, or point
`DM_FFMPEG` at the binary. Settings in the web UI tells you which state you
are in. **Without ffmpeg nothing breaks** — you keep the `.ts`, which plays
fine, and a failed conversion never deletes it.

Details that matter in practice:

- Output is written to `<name>.mp4.part` and renamed on success, so a crash
  or a killed ffmpeg never leaves a truncated file looking finished. The
  `.ts` is deleted only once the `.mp4` is in place under its real name.
- `aac_adtstoasc` is required to put ADTS AAC from a transport stream into
  MP4, but ffmpeg rejects the filter outright for any other audio codec. ODM
  tries with it and falls back without, so an MP2 or AC-3 stream still
  converts.
- `-fflags +genpts`, because TS assembled from HLS segments often has gaps
  in its timestamps.
- A conversion reports no byte progress, so it gets its own `remuxing`
  state rather than showing as a download stalled at 100%. A daemon killed
  mid-conversion marks the record done — the bytes are all fetched and the
  `.ts` plays.

Verified on a real stream: 10:34 output, h264 and aac both preserved, a full
ffmpeg decode pass with zero errors, and `moov` before `mdat`.

## Security

The daemon listens on loopback, which every page in your browser can also
reach, so the API is not open:

- **Shared secret.** A 32-byte token in a `0600` file under `%APPDATA%\odm`.
  Local processes running as you can read it; a web page cannot.
- **Custom auth header.** Requiring `X-ODM-Token` forces a CORS preflight for
  any cross-origin request, which is never approved, so the browser blocks it
  before it is sent.
- **Origin check.** Requests carrying a foreign `Origin` are rejected outright.
- The extension never sees the token — it talks to the daemon through the
  native host, which reads the token from disk.
- Only paths ODM itself chose are handed to the shell for open/reveal; callers
  pass a download id, never a filename.

Verified: no token → 401, wrong token → 401, valid token with a foreign
Origin → 403.

## State

`%APPDATA%\odm` holds `config.json`, `downloads.json`, `token`, `port`,
`extension_key.pem` and the native host manifest. In-progress downloads keep a
`<file>.odm` sidecar next to the output holding the per-segment offsets and the
`ETag`/`Last-Modified` used to prove on resume that the remote file has not
changed. `If-Range` catches the case where it has, rather than splicing two
versions of a file together.

## Tests

```bash
go test ./...
```

Byte-range engine: exact segment tiling (no gaps, no overlaps over an 8 MiB
file), resume after interruption, servers that refuse ranges, unknown-length
streams, fast-fail on 403, pause, stall detection, throughput limiting, and
completing against a concurrency-limited server.

HLS: attribute lists with quoted commas, key inheritance and `METHOD=NONE`,
byte-range segments, `EXT-X-MAP` byte ranges, sequence-derived IVs, ordering
under deliberately staggered responses, measured concurrency, resume
including a torn trailing segment, and live-stream refusal.

Remux: real transport streams built with ffmpeg and converted back, checking
both streams survive, `+faststart` applies, the non-AAC fallback works, a
failed conversion keeps the source, and cancellation is honoured. These skip
when ffmpeg is absent.

The race detector needs cgo, which is unavailable on this machine, so the
suite has not been run under `-race`. Worth doing if you get a C toolchain
installed.

## Not built yet

- DASH (`.mpd`). Detected and refused, not downloaded. It needs MPD parsing
  plus muxing separate audio and video streams, which in practice means a
  hard ffmpeg dependency.
- Choosing an HLS quality level. The highest bandwidth variant is always
  taken; the plumbing for a picker exists but nothing calls it.
- Separate audio renditions. Only the variant stream is fetched, so a
  playlist that keeps audio in a separate `EXT-X-MEDIA` track loses it.
- Quality selection. The highest bandwidth variant is always taken, in the
  panel and everywhere else.
- **Multiple queues.** Everything shares one queue with one concurrency
  limit today.
- **A scheduler.** No "start at 2am" or per-queue timetable yet.
- **A per-download progress window.** Progress is in the list and the web
  UI, but there is no separate window per download.
- **Dark mode for the desktop window.** The web UI follows the system
  theme; the Win32 window does not yet.
- **A native window on macOS and Linux.** Everything except the window and
  the tray builds and runs there already, and `build.ps1` compiles for
  linux/amd64 and darwin/arm64 on every build to keep it that way. The plan
  is to host the existing web UI in a system webview rather than write a
  second and third native UI.

## Releases

Releases are built on GitHub. Tag the commit with the version, matching
`const Version` in `cmd/odm-installer/main.go` and `version` in
`extension/manifest.json`, and push the tag:

```
git tag v0.1.0
git push origin v0.1.0
```

The release workflow refuses a tag that disagrees with either version,
runs `build.ps1`, and publishes `ODM-Setup.exe`, `odm-extension-<version>.zip`,
`odm.crx` and `SHA256SUMS.txt` on the release. Set the repository secret
`EXTENSION_SIGNING_KEY` to the contents of `%APPDATA%\odm\extension_key.pem`
so the extension keeps its id between releases; `build.ps1` passes the key
to `odm-pack` through `DM_EXTENSION_KEY`. CI on every push builds and tests
the Go code on Windows, cross-compiles for Linux and macOS, syntax-checks
the extension and builds the landing page.

## Landing page

The landing page is a Next.js site in `landing/`, exported as static files
and served by Vercel at <https://odm-landing.vercel.app>. Deploy from
`landing/` with `vercel deploy --prod`; the project is linked as
`odm-landing` and `.vercel/` is ignored.

```
cd landing
npm install
npm run dev        # http://localhost:3000
npm run build      # writes the site to landing/out
```

It is draft 20 grown up: black, Geist, a lit hero over the download
window, then numbers, how it works (the range split as an animated bar),
six feature cells, the measurement drawn at real relative speed, four ways
in, the browser hand-off as a log, a FAQ, an install band and a footer.
Rules and backgrounds run edge to edge; text sits in a 1320 px column.

Motion is GSAP, all in `components/Motion.tsx`, driven by data attributes
on server-rendered markup: every animation is a `from`, so the page with
JavaScript off or reduced motion on is the resting state.

Twenty drafts are kept in `site/pages` and served by
`go run ./cmd/odm-site` at <http://127.0.0.1:8090/1> through `/20`.

## License

MIT. See `LICENSE`.
