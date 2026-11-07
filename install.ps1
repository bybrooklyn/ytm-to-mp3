# Install the latest mfetchy release (the mfy command) for Windows.
#   irm https://raw.githubusercontent.com/bybrooklyn/mfetchy/main/install.ps1 | iex
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$tmp = $null
try {
    $repo = 'bybrooklyn/mfetchy'
    $isArm = ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') -or ($env:PROCESSOR_ARCHITEW6432 -eq 'ARM64')
    $arch = if ($isArm) { 'arm64' } else { 'amd64' }

    $ver = if ($env:MFY_VERSION) { $env:MFY_VERSION } else { $env:YTM_VERSION }
    if ($ver) {
        $tag = $ver
        if (-not $tag.StartsWith('v')) { $tag = "v$tag" }
    } else {
        # Resolve via the /releases/latest redirect (no API rate limit).
        $tag = $null
        try {
            $req = [Net.HttpWebRequest]::Create("https://github.com/$repo/releases/latest")
            $req.AllowAutoRedirect = $false
            $resp = $req.GetResponse()
            $loc = $resp.Headers['Location']
            $resp.Close()
            if ($loc -match '/tag/([^/?#]+)$') { $tag = $Matches[1] }
        } catch {
            $tag = $null
        }
        if (-not $tag) {
            throw "could not determine the latest release (check your network). Set `$env:MFY_VERSION = 'vX.Y.Z' and retry."
        }
    }

    if ($tag -notmatch '^v?[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$') {
        throw "invalid version '$tag' (expected something like v1.2.3)"
    }
    if (-not $tag.StartsWith('v')) { $tag = "v$tag" }

    $file = "mfy_$($tag.TrimStart('v'))_windows_$arch.zip"
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
    New-Item -ItemType Directory $tmp | Out-Null

    $base = "https://github.com/$repo/releases/download/$tag"
    if (-not $base.StartsWith('https://')) { throw 'refusing non-https download' }
    Write-Host "Downloading mfetchy $tag (windows/$arch)..."
    try {
        Invoke-WebRequest "$base/$file" -OutFile "$tmp\$file" -UseBasicParsing -MaximumRedirection 5
        Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt" -UseBasicParsing
    } catch {
        throw "download failed for $tag ($file); check your connection and that the release exists. $($_.Exception.Message)"
    }

    $want = ((Get-Content "$tmp\checksums.txt" | Where-Object { $_ -match [regex]::Escape($file) + '$' }) -split '\s+')[0]
    $got = (Get-FileHash "$tmp\$file" -Algorithm SHA256).Hash.ToLower()
    if (-not $want -or $want -ne $got) { throw 'checksum mismatch, refusing to install' }

    $dest = Join-Path $env:LOCALAPPDATA 'Programs\mfetchy'
    New-Item -ItemType Directory $dest -Force | Out-Null
    Expand-Archive "$tmp\$file" -DestinationPath $tmp -Force
    Copy-Item "$tmp\mfy.exe" "$dest\mfy.exe" -Force

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $parts = if ($userPath) { $userPath -split ';' | Where-Object { $_ } } else { @() }
    if ($parts -notcontains $dest) {
        $newPath = if ($userPath) { $userPath.TrimEnd(';') + ";$dest" } else { $dest }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Write-Host "Added $dest to your user PATH."
    }
    if (($env:Path -split ';') -notcontains $dest) { $env:Path = "$env:Path;$dest" }

    Write-Host "Installed to $dest\mfy.exe"
    Write-Host ''
    Write-Host 'Next: run  mfy'
} catch {
    Write-Host "error: $($_.Exception.Message)" -ForegroundColor Red
    $global:LASTEXITCODE = 1
} finally {
    if ($tmp -and (Test-Path $tmp)) { Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue }
}
