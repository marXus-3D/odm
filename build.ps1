# Builds the DM binaries into .\bin and packages the extension into .\dist.
#
# -ldflags "-s -w" strips the symbol table and DWARF data, which cuts each
# binary by roughly a third; there is no cgo here so nothing needs them.
param(
    [switch]$SkipExtension,
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
Set-Location $root

# --- Go binaries -------------------------------------------------------------

New-Item -ItemType Directory -Force -Path bin | Out-Null

$targets = @(
    @{ name = "dm";       pkg = "./cmd/dm" },
    @{ name = "dmd";      pkg = "./cmd/dmd" },
    @{ name = "dm-nmh";   pkg = "./cmd/dm-nmh" },
    @{ name = "dm-setup"; pkg = "./cmd/dm-setup" }
)

foreach ($t in $targets) {
    $out = "bin/$($t.name).exe"
    Write-Host "building $out"
    go build -trimpath -ldflags "-s -w" -o $out $t.pkg
    if ($LASTEXITCODE -ne 0) { throw "build failed for $($t.pkg)" }
}

# A running daemon holds bin\dmd.exe open, so Go renames the old one aside
# rather than failing. Clean up whatever is no longer locked.
Get-ChildItem bin -Filter "*.exe~" -ErrorAction SilentlyContinue | ForEach-Object {
    try { Remove-Item $_.FullName -Force -ErrorAction Stop } catch { }
}

if (-not $SkipTests) {
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
}

# --- Summary -----------------------------------------------------------------

Write-Host ""
Get-ChildItem bin -Filter *.exe |
    Select-Object Name, @{n = "MB"; e = { [math]::Round($_.Length / 1MB, 1) } } |
    Format-Table -AutoSize

if (-not $SkipExtension) {
    Get-ChildItem dist -Filter *.zip -ErrorAction SilentlyContinue |
        Select-Object Name, @{n = "KB"; e = { [math]::Round($_.Length / 1KB, 1) } } |
        Format-Table -AutoSize
}
