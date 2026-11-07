# Security policy

## Reporting a vulnerability

Please use GitHub private vulnerability reporting:
<https://github.com/bybrooklyn/mfetchy/security/advisories/new>

Do not open a public issue for security problems. I will acknowledge reports
as soon as I can and aim to ship a fix in the next release.

## Supported versions

Only the latest release receives security fixes.

## What ytm stores locally

Config lives in your user config directory (the Settings screen shows the path), caches in your
user cache directory under `ytm`.

- `config.json`: your settings (music folder, format, number of VPN connections, whether
  to use a browser login or the VPN). No secrets.
- `proton.json`: the Proton VPN session (access/refresh tokens and the WireGuard key
  seed), written atomically with mode 0600 in a 0700 directory. Anyone who can read it can
  use your Proton session; signing out in Settings revokes the session at Proton and deletes the file.
  Your password is never written to disk. On Windows the 0600 mode has no effect and the
  folder's normal permissions protect the file. The device key stays listed in your Proton
  account until you remove it at account.protonvpn.com.
- No browser cookies or Google login are ever read. (Older versions' `cookies.txt` is deleted on the next run.)
- `<music folder>/.ytm/state.db`: which tracks were downloaded or failed, with titles and
  error text (mode 0600).
- `bin/` (cache dir): yt-dlp, ffmpeg, ffprobe and deno if ytm had to download them. Each is
  checked against the checksum file published in the same release (this catches corrupt or
  truncated downloads, but not a compromised release; the macOS Intel ffmpeg build has no
  published checksum and is only a last resort). ytm records a hash of each binary and
  re-downloads one that later changes.

## What ytm contacts

- YouTube / YouTube Music, via yt-dlp.
- GitHub, to download and update helper tools and releases.
- Proton's API (SPKI-pinned) when you use the Proton VPN feature.
- MusicBrainz, for tag lookups (Fix tags can also work offline).
- `api.ipify.org`, only when you use Test VPN in Settings (to show your public IP).

There is no telemetry.

## Release integrity

Releases include `checksums.txt`, signed with cosign keyless
(`checksums.txt.sig` and `checksums.txt.pem`), plus a GitHub build provenance
attestation. See the comment at the top of `install.sh` for the verify command.
