# DM

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

DM instead starts with a single whole-file range. Each idle worker splits the
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
exactly this way, and DM finishes the transfer instead of failing.

A connection that stops delivering bytes for 30s is dropped and its range
retried on a fresh socket, so a black-holed route cannot pin a worker.

## Layout

```
cmd/dm         CLI: standalone downloads, and a client for the daemon
cmd/dmd        the app: desktop window, tray, queue, web UI, HTTP API
cmd/dm-nmh     Chrome native messaging host
cmd/dm-setup   installs the browser integration
internal/engine   byte-range segments, dynamic splitting, resume, retry
internal/hls      M3U8 parsing, parallel segment fetch, AES-128, remux
internal/manager  queue, concurrency limits, engine selection
internal/api      HTTP API + embedded web UI
internal/nativeui the Win32 desktop window
internal/trayicon notification-area icon
internal/client   shared daemon client (CLI, window and native host)
internal/store    persisted download list and config
extension/        MV3 Chrome extension
```

## Build

```powershell
.\build.ps1
```

That builds the four binaries into `.\bin`, runs the tests, validates the
extension and packages it to `.\dist\dm-extension-<version>.zip`. Use
`-SkipTests` or `-SkipExtension` to do less.

Go 1.22+ is required (the API uses method-aware `http.ServeMux` patterns).
`ffmpeg` is optional; see "TS to MP4".

## Use it

### CLI

Download right now, in this process:

```bash
./bin/dm.exe -n 8 https://example.com/big.iso
```

Ctrl-C pauses and writes resume state; rerun the same command to continue.
Flags: `-n` connections, `-d` directory, `-o` filename, `-limit` KiB/s,
`-referer`, `-cookie`, `-ua`, `-q`.

Or drive the daemon, which starts on demand:

```bash
./bin/dm.exe add https://example.com/big.iso
./bin/dm.exe ls
./bin/dm.exe pause <id>
./bin/dm.exe resume <id>
./bin/dm.exe rm -f <id>
./bin/dm.exe limit 500        # KiB/s across everything; "off" to remove
./bin/dm.exe open <id>        # or "show" to reveal in Explorer
./bin/dm.exe ui               # print the web UI url
./bin/dm.exe daemon stop      # graceful: pauses downloads, saves state
```

### The app

Double-click `bin\dmd.exe`, or run it:

```bash
./bin/dmd.exe
```

That opens the desktop window: a real Win32 application with a menu bar, a
toolbar, and a list showing name, size, progress, speed, status and time
left. Progress is a drawn bar coloured by state, not a number. Double click
opens a finished download and pauses a running one, right click gives the
same actions as the menus, and several rows can be selected at once.

Closing the window leaves the daemon running and the icon in the
notification area, the way a download manager should behave. **File → Exit**
or the tray's **Quit DM** stops it properly. The tray's **Open DM** brings
the window back; left-clicking the tray icon does the same.

> On Windows 11 new tray icons start hidden. If you cannot see it, click the
> `^` next to the clock, or turn it on under Settings → Personalisation →
> Taskbar → Other system tray icons.

There is no separate GUI binary and no dependency behind this: the window is
Win32 through `syscall`, for the same reason as the tray, so `dmd.exe` is
still one self-contained executable.

`-no-window` runs it headless, `-open` uses the browser UI instead.

### Web UI

The web UI is still there and does everything the window does, plus
settings. **File → Open web UI**, the tray menu, or:

```bash
./bin/dm.exe ui
```

It shows live speed, connection count and a per-segment progress map, and
supports pause, resume, remove, open and show-in-folder.

The daemon also starts on demand — the extension launches it if it is not
already running, so you do not have to keep it running yourself. Started
that way it does not open a browser tab.

`dm.exe` is the command line client, not the app — double-clicking it just
prints its usage and exits.

`dmd` is a GUI binary, so it does not flash a console window, but it still
prints normally when run from a terminal, and always logs to
`%APPDATA%\dm\dmd.log`.

### Browser extension

```powershell
.\bin\dm-setup.exe
```

That generates a keypair so the extension has a stable id, writes the native
messaging host manifest, and registers it for Chrome, Edge, Brave, Chromium,
Vivaldi and Opera. Then:

1. Open `chrome://extensions`
2. Enable **Developer mode**
3. **Load unpacked** → the `extension` folder
4. Check the id matches the one `dm-setup` printed

Reload the extension after any rebuild that changes it.

Undo with `.\bin\dm-setup.exe -uninstall` plus removing the extension.

**If you get "Access to the specified native messaging host is forbidden"**,
the extension's id and the id in the host manifest have diverged. Rerun
`dm-setup`, then press Reload on the extension card.

```powershell
.\bin\dm-setup.exe -check
```

prints the id in the extension manifest, the ids the host manifest allows,
the id each browser actually loaded it under, and the registry entries, and
exits non-zero naming the one that is wrong.

The id comes from the `key` in the extension manifest, so it survives moving
the folder — and `dm-setup` reads the id back out of that manifest after
writing it, rather than deriving it from the key file, because Chrome only
ever sees the manifest. It also allows the ids browsers report for this
extension and the path-derived ids Chrome uses when a manifest has no key,
so an extension loaded before the key was added keeps working.

**What the extension does.** It watches `chrome.downloads.onCreated`, cancels
anything matching your rules, and forwards the URL to the daemon along with
the tab's cookies (including HttpOnly ones), `Referer` and `User-Agent`.
Without those, an outside process fetching a logged-in download just gets a
403. There is also a "Download with DM" context-menu item for links, images,
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

## Streaming video

Give DM an `.m3u8` URL -- from the extension popup, the web UI or
`dm add` -- and it is recognised automatically and fetched as a playlist
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
editors will not open one, so DM converts it.

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
  MP4, but ffmpeg rejects the filter outright for any other audio codec. DM
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

- **Shared secret.** A 32-byte token in a `0600` file under `%APPDATA%\dm`.
  Local processes running as you can read it; a web page cannot.
- **Custom auth header.** Requiring `X-DM-Token` forces a CORS preflight for
  any cross-origin request, which is never approved, so the browser blocks it
  before it is sent.
- **Origin check.** Requests carrying a foreign `Origin` are rejected outright.
- The extension never sees the token — it talks to the daemon through the
  native host, which reads the token from disk.
- Only paths DM itself chose are handed to the shell for open/reveal; callers
  pass a download id, never a filename.

Verified: no token → 401, wrong token → 401, valid token with a foreign
Origin → 403.

## State

`%APPDATA%\dm` holds `config.json`, `downloads.json`, `token`, `port`,
`extension_key.pem` and the native host manifest. In-progress downloads keep a
`<file>.dm` sidecar next to the output holding the per-segment offsets and the
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
