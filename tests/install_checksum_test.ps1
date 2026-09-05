$ErrorActionPreference = "Stop"
$installer = Join-Path $PSScriptRoot "../scripts/install.ps1"
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($installer, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw "installer syntax errors: $parseErrors" }
$function = $ast.Find({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq "Test-ReleaseChecksum" }, $true)
if (-not $function) { throw "checksum verifier missing" }
. ([scriptblock]::Create($function.Extent.Text))

$testDir = Join-Path ([IO.Path]::GetTempPath()) ("galley-checksum-test-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory $testDir | Out-Null
try {
  $asset = "galley_1.2.3_windows_amd64.tar.gz"
  $archive = Join-Path $testDir $asset
  $manifest = Join-Path $testDir "checksums.txt"
  [IO.File]::WriteAllText($archive, "release fixture")
  $digest = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash
  $valid = "$digest  $asset"
  $cases = @{
    valid = $valid
    mismatch = (("0" * 64) + "  $asset")
    absent = "$digest  other.tar.gz"
    duplicate = "$valid`n$valid"
    malformed = "bad-hash  $asset"
  }
  foreach ($name in $cases.Keys) {
    [IO.File]::WriteAllText($manifest, $cases[$name])
    $failure = $null
    try { Test-ReleaseChecksum $archive $asset $manifest } catch { $failure = $_ }
    if ($name -eq "valid" -and $failure) { throw $failure }
    if ($name -ne "valid" -and -not $failure) { throw "$name unexpectedly verified" }
    Write-Host "PowerShell checksum ${name}: passed"
  }
} finally {
  Remove-Item -LiteralPath $testDir -Recurse -Force
}
