# DM

An IDM-style download manager for Windows: parallel range downloads with
dynamic segmentation, resume across restarts, a local web UI, and a Chrome
extension that hands the browser's downloads over — cookies and all.

Written in Go, no external dependencies. The idle daemon sits at about 9 MB
of RSS.

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
cmd/dm         standalone CLI downloader
cmd/dmd        daemon: queue, web UI, HTTP API
cmd/dm-nmh     Chrome native messaging host
cmd/dm-setup   installs the browser integration
internal/engine   segments, dynamic splitting, resume, retry
internal/manager  queue and concurrency limits
internal/api      HTTP API + embedded web UI
internal/store    persisted download list and config
extension/        MV3 Chrome extension
```

## Build

```powershell
.\build.ps1
```

Binaries land in `.\bin`. Go 1.22+ is required (the API uses method-aware
`http.ServeMux` patterns).

## Use it

### CLI

```bash
./bin/dm.exe -n 8 https://example.com/big.iso
```

Ctrl-C pauses and writes resume state; rerun the same command to continue.
Flags: `-n` connections, `-d` directory, `-o` filename, `-referer`, `-cookie`,
`-ua`, `-q`.

### Daemon and web UI

```bash
./bin/dmd.exe
```

Then open <http://127.0.0.1:9111/>. The UI shows live speed, connection count
and a per-segment progress map, and supports pause, resume, remove, open and
show-in-folder.

The daemon also starts on demand — the extension launches it if it is not
already running, so you do not have to keep it running yourself.

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

Undo with `.\bin\dm-setup.exe -uninstall` plus removing the extension.

**What the extension does.** It watches `chrome.downloads.onCreated`, cancels
anything matching your rules, and forwards the URL to the daemon along with
the tab's cookies (including HttpOnly ones), `Referer` and `User-Agent`.
Without those, an outside process fetching a logged-in download just gets a
403. There is also a "Download with DM" context-menu item for links, images,
video and audio.

Settings (extension options page): on/off, minimum size, whether to grab
unknown-size downloads, file types to ignore, notifications.

If the daemon cannot be reached the extension says so and lets Chrome do the
download normally, rather than losing it.

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

Covers exact segment tiling (no gaps, no overlaps over an 8 MiB file),
resume after interruption, servers that refuse ranges, unknown-length streams,
fast-fail on 403, pause, stall detection, and completing against a
concurrency-limited server.

The race detector needs cgo, which is unavailable on this machine, so the
suite has not been run under `-race`. Worth doing if you get a C toolchain
installed.

## Not built yet

- HLS/DASH video grabbing (the `.m3u8` / `.mpd` "download this video" panel)
- Speed limiting
- `dm` CLI subcommands that talk to the running daemon (it is standalone today)
- A system tray icon
