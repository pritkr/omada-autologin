# One-command installer for omada-autologin on Windows.
#
#   irm https://raw.githubusercontent.com/pritkr/omada-autologin/main/install.ps1 | iex
#
# Or download and run:
#   powershell -ExecutionPolicy Bypass -File install.ps1
param(
    [string]$Version = "latest",
    [string]$InstallDir = "$env:LOCALAPPDATA\omada-autologin\bin",
    [switch]$NoService,
    [switch]$NoLogin
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$repo = "pritkr/omada-autologin"
$bin = "omada-autologin"
$script:NativeExit = 0

# Native programs (especially `status` when no config exists) write to stderr.
# With ErrorActionPreference=Stop, PowerShell 5.1 can turn that into a
# terminating error, so run them with Continue and check the exit code.
function Invoke-Native {
    param(
        [string]$FilePath,
        [string[]]$ArgumentList
    )
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & $FilePath @ArgumentList
    }
    finally {
        $script:NativeExit = $LASTEXITCODE
        $ErrorActionPreference = $previous
    }
}

$osArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
$arch = switch ($osArch) {
    "X64" { "amd64" }
    "Arm64" { "arm64" }
    default { throw "unsupported architecture: $osArch" }
}

try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
} catch {
    # Newer .NET versions ignore or deprecate this; TLS 1.2 is already the default.
}

if ($Version -eq "latest") {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" `
        -Headers @{ "User-Agent" = "omada-autologin-installer" }
    $Version = $release.tag_name
}
if (-not $Version) { throw "could not determine the latest release" }
if (-not $Version.StartsWith("v")) { $Version = "v$Version" }

$asset = "${bin}_${Version}_windows_${arch}.zip"
$base = "https://github.com/$repo/releases/download/$Version"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("omada-install-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
    Write-Host "downloading $bin $Version (windows/$arch)"
    Invoke-WebRequest -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset)
    Invoke-WebRequest -Uri "$base/SHA256SUMS.txt" -OutFile (Join-Path $tmp "SHA256SUMS.txt")

    $pattern = "  $([regex]::Escape($asset))$"
    $line = Select-String -Path (Join-Path $tmp "SHA256SUMS.txt") -Pattern $pattern | Select-Object -First 1
    if (-not $line) { throw "checksum for $asset is missing" }
    $expected = ($line.Line -split '\s+')[0].ToLower()
    $actual = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $asset)).Hash.ToLower()
    if ($expected -ne $actual) { throw "checksum mismatch for $asset" }

    Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath $tmp -Force
    $src = Join-Path $tmp "${bin}_${Version}_windows_${arch}\${bin}.exe"
    if (-not (Test-Path $src)) { throw "binary not found in the archive" }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    $exe = Join-Path $InstallDir "$bin.exe"
    Copy-Item $src $exe -Force
    Write-Host "installed $exe"

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $parts = @()
    if ($userPath) { $parts = @($userPath -split ';' | Where-Object { $_ }) }
    if ($parts -notcontains $InstallDir) {
        [Environment]::SetEnvironmentVariable("Path", (($parts + $InstallDir) -join ';'), "User")
        Write-Host "added $InstallDir to the user PATH (new terminals only)"
    }
    $env:Path = "$InstallDir;$env:Path"

    $configured = $false
    Invoke-Native $exe @("status") *> $null
    if ($script:NativeExit -eq 0) {
        $configured = $true
        Write-Host "existing config found; keeping it"
    }
    elseif ($env:OMADA_PASSWORD) {
        $initArgs = @("init", "--password", $env:OMADA_PASSWORD)
        if ($env:OMADA_HOST) { $initArgs += @("--host", $env:OMADA_HOST) }
        if ($env:OMADA_USERNAME) { $initArgs += @("--username", $env:OMADA_USERNAME) }
        Invoke-Native $exe $initArgs
        if ($script:NativeExit -ne 0) { throw "config setup failed" }
        $configured = $true
    }
    else {
        Invoke-Native $exe @("init")
        if ($script:NativeExit -eq 0) { $configured = $true }
    }

    if ($configured -and -not $NoLogin) {
        Write-Host "testing login..."
        Invoke-Native $exe @("once", "--verbose")
        if ($script:NativeExit -ne 0) {
            Write-Warning "first login did not succeed; check the credentials and portal host"
        }
    }
    if ($configured -and -not $NoService) {
        Write-Host "installing scheduled task..."
        Invoke-Native $exe @("install")
        if ($script:NativeExit -ne 0) {
            throw "service install failed; you can still run `"$exe run`" manually or from Task Scheduler"
        }
    }

    Write-Host "done"
    if ($configured) {
        Write-Host "check: $exe status"
        Write-Host "logs:  $env:LOCALAPPDATA\omada-autologin\omada-autologin.log"
    }
}
finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
