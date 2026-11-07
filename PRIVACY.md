# Privacy

What ytm sends, to whom, what it keeps on your computer, and what it never sends. ytm has no server of its own: no account, no telemetry, no analytics, no update ping.

`ytm doctor` (More tools > Check that everything works) shows your current VPN and private-mode state and warns about secret files other users could read.

## Who ytm talks to

"Through the VPN" means: through ytm's own userspace WireGuard tunnel, and names are looked up inside the tunnel (SOCKS5 with remote DNS), never by your system resolver. If a VPN is set up and turned on and it can't connect, ytm stops. It does not fall back to your own IP.

| Host | When | What it receives | Through the VPN |
|---|---|---|---|
| YouTube: `www.youtube.com`, `music.youtube.com`, `youtubei`, `*.googlevideo.com`, `i.ytimg.com` (also via yt-dlp) | every YouTube / YouTube Music link, library and download | the video, playlist or album ids you ask for; the stream requests; a generic browser User-Agent | yes |
| Apple Music: `music.apple.com`, `amp-api.music.apple.com`, `*.mzstatic.com` | only if you signed in to Apple Music or paste an Apple link | song ids and search text (title, artist), your Apple login (`media-user-token`, cookies) in the headers; downloads of the audio and artwork | yes |
| Spotify: `open.spotify.com`, `i.scdn.co` | only for Spotify links | the track / album / playlist id from the link; cover images | yes |
| MusicBrainz: `musicbrainz.org`, `coverartarchive.org` | tag fixing after a download and "Fix tags" (not in private mode) | artist, title, album and duration of the songs being tagged; User-Agent `ytm-to-mp3/1` with the project URL (MusicBrainz requires one) | yes |
| GitHub: `github.com` (releases of yt-dlp, FFmpeg-Builds, deno), `evermeet.cx` (macOS ffmpeg) | first run, and when yt-dlp is out of date | nothing about you beyond a download request and the User-Agent `ytm-to-mp3`; your IP | no (needed before the VPN is up; it carries no listening data) |
| Your VPN provider's API: `vpn-api.proton.me` (Proton), `api.mullvad.net` and `ipv4.am.i.mullvad.net` (Mullvad) | sign-in, server list, registering a WireGuard key | see below | no (the tunnel doesn't exist yet) |
| VPN servers | while downloading | your encrypted traffic (WireGuard) | that is the tunnel |
| `api.ipify.org` | only Settings > Test VPN | nothing; the answer is the exit IP | yes (that is the point) |
| Any other URL you pass to ytm | when you do | whatever yt-dlp sends for that site (SoundCloud, Bandcamp, ...) | yes |

Not contacted: any lyrics service (none exists in ytm yet), any analytics or crash-report service, any ytm server.

### Your VPN provider

- **Proton VPN**: your email or username and an SRP proof of your password (the password itself never leaves your computer and is never saved), a 2FA code if you use one, and the public part of each WireGuard key. ytm presents itself with the same app headers Proton's own Linux client uses. No device name is sent, so your hostname isn't leaked.
- **Mullvad**: your account number (in the body of requests to `api.mullvad.net` only, never in a URL), and the public part of each WireGuard key. Mullvad names the devices itself. `ipv4.am.i.mullvad.net` is asked once for your approximate location so nearby servers are preferred; it only sees the IP address you are about to hide. Mullvad allows 5 devices per account; ytm uses one key per connection, never goes above the free slots and removes its keys when you sign out.
- **Your own WireGuard config**: nothing is sent to any API. Config files that name their server by host name need one lookup of that name before the tunnel is up; that lookup uses your system resolver. Use an IP address in `Endpoint` to avoid it.

The sign-in and server-list requests go from your own IP because the tunnel doesn't exist yet.

## What ytm stores, and where

All in your user folders (`~/.config/ytm` and `~/.cache/ytm` on Linux; the matching folders on macOS and Windows).

| File | Holds | Mode |
|---|---|---|
| `config.json` | your settings (music folder, format, VPN provider, private mode...) | 0600 |
| `proton.json` | Proton session tokens and WireGuard key seeds. Never the password. | 0600 |
| `mullvad.json` | Mullvad account number and the WireGuard private keys ytm registered | 0600 |
| `wireguard/*.conf` | copies of the WireGuard configs you imported (they hold private keys) | 0600 |
| `apple-token`, `apple-cookies` | Apple Music login | 0600 |
| `premium-cookies.txt` | YouTube Premium cookies, only if you turn that feature on | 0600 |
| `~/.cache/ytm/servers*.json` | VPN server lists and speed / block history per server (no credentials) | 0600 |
| `<music folder>/.ytm/` | which songs were downloaded, the tag-fix undo journal, cached MusicBrainz answers | folder 0700 |

Folders are 0700. Files holding secrets are replaced, not edited in place, so an old file with looser permissions is tightened the next time it is saved. Secrets are never put on a command line or in a URL, and never written to logs. `YTM_DEBUG=1` prints timing and Proton's error replies with token-like strings masked; proxy credentials are masked in tool output. The only credential on a command line is the random, local-only password of the loopback SOCKS5 proxy in front of each tunnel (127.0.0.1, one per tunnel), which is useless anywhere else.

Browser cookies are read only when you ask for them: the Premium feature (YouTube cookies) and "sign in to Apple Music from my browser" (Apple cookies). Nothing else looks at your browser.

Your music files carry the tags you'd expect (title, artist, album...) and a comment with the YouTube id. They contain no information about you.

## What is never sent

- Your VPN password or Proton password (Proton gets a cryptographic proof, not the password).
- Your Mullvad account number to anyone but `api.mullvad.net`.
- Your music library, listening history or file names to anyone but the music source you asked for, and (if private mode is off) the artist / title / album of songs being tagged to MusicBrainz.
- Your computer's name, user name or file paths.

## Controls

- **Private mode** (Settings): no MusicBrainz or cover-art lookups. Tags come only from the source you downloaded from. (`ytm tag --offline` does the same for one run.)
- **VPN** (Settings > VPN account): Proton, Mullvad, or your own WireGuard config. `--no-vpn` skips it for one run; with the VPN on and unable to connect, ytm stops.
- Sign out removes the local credentials, and for Mullvad also the device keys from your account. For Proton the WireGuard keys stay listed until you remove them at account.protonvpn.com.
- Everything stored is plain files you can delete; nothing else is kept anywhere.
