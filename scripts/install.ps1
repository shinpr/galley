param(
  [string]$Version = $(if ($env:GALLEY_VERSION) { $env:GALLEY_VERSION } else { "latest" }),
  [string]$BinDir = $(if ($env:GALLEY_BIN_DIR) { $env:GALLEY_BIN_DIR } else { Join-Path $HOME ".local\bin" })
)

$ErrorActionPreference = "Stop"

$Owner = "shinpr"
$Repo = "galley"

function Resolve-LatestVersion {
  $release = Invoke-RestMethod "https://api.github.com/repos/$Owner/$Repo/releases/latest"
  return $release.tag_name
}

function Resolve-Arch {
  $arch = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
  switch ($arch) {
    "Arm64" { return "arm64" }
    "X64" { return "amd64" }
    default { throw "unsupported architecture: $arch" }
  }
}

function Stop-ExistingDaemon {
  param([string]$GalleyBin)

  if (-not (Test-Path $GalleyBin)) {
    return
  }

  try {
    $statusText = & $GalleyBin daemon status --output json 2>$null
    $status = $statusText | ConvertFrom-Json
  } catch {
    return
  }

  if ($status.running -and $status.verified) {
    Write-Host "Existing Galley daemon is running; stopping it before install..."
    & $GalleyBin daemon stop
    if ($LASTEXITCODE -ne 0) {
      throw "could not stop existing Galley daemon; run `"$GalleyBin daemon stop`" and retry"
    }
  }
}

if ($Version -eq "latest") {
  $Version = Resolve-LatestVersion
  if (-not $Version) {
    throw "could not resolve latest Galley release"
  }
}

$assetVersion = $Version.TrimStart("v")
$arch = Resolve-Arch
$asset = "galley_${assetVersion}_windows_${arch}.tar.gz"
$url = "https://github.com/$Owner/$Repo/releases/download/$Version/$asset"
$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("galley-install-" + [System.Guid]::NewGuid().ToString("N"))
$archive = Join-Path $tmpDir $asset
$dest = Join-Path $BinDir "galley.exe"

New-Item -ItemType Directory -Force $tmpDir | Out-Null

try {
  New-Item -ItemType Directory -Force $BinDir | Out-Null

  Write-Host "Downloading $url"
  Invoke-WebRequest $url -OutFile $archive -UseBasicParsing

  tar.exe -xzf $archive -C $tmpDir galley.exe
  $src = Join-Path $tmpDir "galley.exe"
  if (-not (Test-Path $src)) {
    throw "release asset did not contain galley.exe"
  }

  Stop-ExistingDaemon $dest
  Copy-Item $src $dest -Force

  & $dest --help | Out-Null

  $pathEntries = [Environment]::GetEnvironmentVariable("Path", "User") -split ";"
  if ($pathEntries -notcontains $BinDir) {
    Write-Host ""
    Write-Host "Installed $dest, but $BinDir is not on your user PATH."
    Write-Host "Add it with:"
    Write-Host "  [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ';$BinDir', 'User')"
    Write-Host "Open a new terminal after updating PATH."
  }

  Write-Host "Galley installed: $dest"
} finally {
  if (Test-Path $tmpDir) {
    Remove-Item -Recurse -Force $tmpDir
  }
}
