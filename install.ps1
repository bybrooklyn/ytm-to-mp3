# Install the latest ytm release for Windows.
#   irm https://raw.githubusercontent.com/bybrooklyn/ytm-to-mp3/main/install.ps1 | iex
$ErrorActionPreference = 'Stop'
$repo = 'bybrooklyn/ytm-to-mp3'
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$rel = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
$tag = $rel.tag_name
$file = "ytm_$($tag.TrimStart('v'))_windows_$arch.zip"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null
Write-Host "Downloading ytm $tag (windows/$arch)..."
Invoke-WebRequest "https://github.com/$repo/releases/download/$tag/$file" -OutFile "$tmp\$file"
Invoke-WebRequest "https://github.com/$repo/releases/download/$tag/checksums.txt" -OutFile "$tmp\checksums.txt"
$want = ((Get-Content "$tmp\checksums.txt" | Where-Object { $_ -match [regex]::Escape($file) + '$' }) -split '\s+')[0]
$got = (Get-FileHash "$tmp\$file" -Algorithm SHA256).Hash.ToLower()
if (-not $want -or $want -ne $got) { throw 'checksum mismatch' }
$dest = Join-Path $env:LOCALAPPDATA 'Programs\ytm'
New-Item -ItemType Directory $dest -Force | Out-Null
Expand-Archive "$tmp\$file" -DestinationPath $tmp -Force
Copy-Item "$tmp\ytm.exe" "$dest\ytm.exe" -Force
$path = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($path -notlike "*$dest*") { [Environment]::SetEnvironmentVariable('Path', "$path;$dest", 'User'); Write-Host "Added $dest to your PATH (open a new terminal)." }
Remove-Item $tmp -Recurse -Force
Write-Host "Installed to $dest\ytm.exe"
