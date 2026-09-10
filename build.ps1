# Builds every DM binary into .\bin.
# -ldflags "-s -w" strips the symbol table and DWARF data, which cuts each
# binary by roughly a third; there is no cgo here so nothing needs them.
$ErrorActionPreference = "Stop"

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

Write-Host ""
Get-ChildItem bin -Filter *.exe |
    Select-Object Name, @{n = "MB"; e = { [math]::Round($_.Length / 1MB, 1) } } |
    Format-Table -AutoSize
