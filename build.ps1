# Builds the DM binaries into .\bin and packages the extension into .\dist.
#
# -ldflags "-s -w" strips the symbol table and DWARF data, which cuts each
# binary by roughly a third; there is no cgo here so nothing needs them.
param(
    [switch]$SkipExtension,
    [switch]$SkipTests,
    [switch]$SkipInstaller
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
Set-Location $root

# --- Go binaries -------------------------------------------------------------

New-Item -ItemType Directory -Force -Path bin | Out-Null

# dmd and dm-nmh are linked as GUI binaries (-H windowsgui) so that
# double-clicking dmd, and Chrome spawning dm-nmh, do not flash a console
# window. dmd reattaches to the parent console when it is run from a terminal,
# so command line use still prints normally.
$targets = @(
    @{ name = "dm";       pkg = "./cmd/dm";       gui = $false },
    @{ name = "dmd";      pkg = "./cmd/dmd";      gui = $true  },
    @{ name = "dm-nmh";   pkg = "./cmd/dm-nmh";   gui = $true  },
    @{ name = "dm-setup"; pkg = "./cmd/dm-setup"; gui = $false }
)

foreach ($t in $targets) {
    $out = "bin/$($t.name).exe"
    $ldflags = "-s -w"
    if ($t.gui) { $ldflags = "$ldflags -H windowsgui" }
    Write-Host "building $out"
    go build -trimpath -ldflags $ldflags -o $out $t.pkg
    if ($LASTEXITCODE -ne 0) { throw "build failed for $($t.pkg)" }
}

# A running daemon holds bin\dmd.exe open, so Go renames the old one aside
# rather than failing. Clean up whatever is no longer locked.
Get-ChildItem bin -Filter "*.exe~" -ErrorAction SilentlyContinue | ForEach-Object {
    try { Remove-Item $_.FullName -Force -ErrorAction Stop } catch { }
}

if (-not $SkipTests) {
    # The daemon, engine, CLI and web UI are meant to build everywhere; only
    # the desktop window and tray are Windows-only. Compiling for the other
    # platforms catches a stub drifting out of step with the real one.
    Write-Host "`nchecking cross-platform builds"
    foreach ($t in @("linux/amd64", "darwin/arm64")) {
        $parts = $t.Split("/")
        $env:GOOS = $parts[0]; $env:GOARCH = $parts[1]
        go build ./... 2>&1 | Write-Host
        $failed = $LASTEXITCODE -ne 0
        Remove-Item Env:GOOS, Env:GOARCH
        if ($failed) { throw "build failed for $t" }
        Write-Host "  $t ok"
    }

    Write-Host "`nrunning tests"
    go test ./... 2>&1 | Where-Object { $_ -notmatch "no test files" } | Write-Host
    if ($LASTEXITCODE -ne 0) { throw "tests failed" }
}

# --- Extension ---------------------------------------------------------------

if (-not $SkipExtension) {
    Write-Host "`nvalidating extension"

    $extDir = Join-Path $root "extension"
    $manifestPath = Join-Path $extDir "manifest.json"
    if (-not (Test-Path $manifestPath)) { throw "no manifest.json in $extDir" }

    try {
        $manifest = Get-Content $manifestPath -Raw | ConvertFrom-Json
    } catch {
        throw "manifest.json is not valid JSON: $_"
    }

    # Every path the manifest names must exist, or Chrome rejects the whole
    # extension with an unhelpful error at load time.
    $referenced = New-Object System.Collections.Generic.List[string]
    if ($manifest.background.service_worker) { $referenced.Add($manifest.background.service_worker) }
    if ($manifest.action.default_popup)      { $referenced.Add($manifest.action.default_popup) }
    if ($manifest.options_page)              { $referenced.Add($manifest.options_page) }
    foreach ($set in @($manifest.icons, $manifest.action.default_icon)) {
        if ($set) {
            foreach ($p in $set.PSObject.Properties) { $referenced.Add($p.Value) }
        }
    }

    $missing = @()
    foreach ($rel in ($referenced | Select-Object -Unique)) {
        if (-not (Test-Path (Join-Path $extDir $rel))) { $missing += $rel }
    }
    if ($missing.Count -gt 0) {
        throw "manifest references files that do not exist: $($missing -join ', ')"
    }

    if (-not $manifest.key) {
        Write-Host "  warning: manifest has no 'key'; run bin\dm-setup.exe so the extension id is stable"
    }

    # Syntax-check the scripts when node is around. A parse error in the
    # service worker silently disables the whole extension.
    $node = Get-Command node -ErrorAction SilentlyContinue
    if ($node) {
        foreach ($js in Get-ChildItem $extDir -Filter *.js) {
            & node --check $js.FullName
            if ($LASTEXITCODE -ne 0) { throw "syntax error in $($js.Name)" }
        }
        Write-Host "  js syntax ok"
    } else {
        Write-Host "  node not found; skipped js syntax check"
    }

    New-Item -ItemType Directory -Force -Path dist | Out-Null
    $zip = Join-Path $root "dist/dm-extension-$($manifest.version).zip"
    if (Test-Path $zip) { Remove-Item $zip -Force }
    Compress-Archive -Path (Join-Path $extDir "*") -DestinationPath $zip
    Write-Host "  packaged $zip"

    # Sign the extension so the installer can offer it to browsers as a
    # real .crx. This also writes the public key into manifest.json, which
    # is what fixes the extension id.
    # On a build machine the key comes from DM_EXTENSION_KEY (a path to the
    # PEM); the release workflow writes it there from a repository secret so
    # the extension id stays the same across releases.
    $crx = Join-Path $root "dist/dm.crx"
    $packArgs = @("-extension", $extDir, "-out", $crx)
    if ($env:DM_EXTENSION_KEY) { $packArgs += @("-key", $env:DM_EXTENSION_KEY) }
    go run ./cmd/dm-pack @packArgs 2>&1 | Write-Host
    if ($LASTEXITCODE -ne 0) { throw "packing the extension failed" }
}

# --- Setup program -----------------------------------------------------------

if (-not $SkipInstaller) {
    Write-Host "`nstaging the installer payload"

    $payload = Join-Path $root "cmd/dm-installer/payload"
    # Rebuild the payload from scratch: a stale binary left behind here
    # would be shipped silently.
    Get-ChildItem $payload -Exclude ".gitkeep" -Force -ErrorAction SilentlyContinue |
        Remove-Item -Recurse -Force
    New-Item -ItemType Directory -Force -Path (Join-Path $payload "bin") | Out-Null

    foreach ($t in $targets) {
        Copy-Item "bin/$($t.name).exe" (Join-Path $payload "bin") -Force
    }
    Copy-Item (Join-Path $root "extension") $payload -Recurse -Force
    Get-ChildItem (Join-Path $payload "extension") -Filter *.pem -Recurse -Force -ErrorAction SilentlyContinue |
        Remove-Item -Force
    $crx = Join-Path $root "dist/dm.crx"
    if (Test-Path $crx) { Copy-Item $crx $payload -Force }

    Write-Host "building dist/DM-Setup.exe"
    go build -trimpath -ldflags "-s -w -H windowsgui" -o dist/DM-Setup.exe ./cmd/dm-installer
    if ($LASTEXITCODE -ne 0) { throw "build failed for the installer" }
}

# --- Summary -----------------------------------------------------------------

Write-Host ""
Get-ChildItem bin -Filter *.exe |
    Select-Object Name, @{n = "MB"; e = { [math]::Round($_.Length / 1MB, 1) } } |
    Format-Table -AutoSize

if (-not $SkipInstaller) {
    Get-ChildItem dist -Filter *.exe -ErrorAction SilentlyContinue |
        Select-Object Name, @{n = "MB"; e = { [math]::Round($_.Length / 1MB, 1) } } |
        Format-Table -AutoSize
}

if (-not $SkipExtension) {
    Get-ChildItem dist -Filter *.zip -ErrorAction SilentlyContinue |
        Select-Object Name, @{n = "KB"; e = { [math]::Round($_.Length / 1KB, 1) } } |
        Format-Table -AutoSize
}
