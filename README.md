# ytm

Your music library from YouTube Music, Apple Music and Spotify, as properly tagged audio files. One small binary for Windows, macOS and Linux.

```
ytm                                # open ytm: everything is on one screen
ytm "https://youtu.be/..."         # or download a link straight away (keep the quotes)
```

There are no subcommands to remember. Plain `ytm` opens a full-screen home:

- **A box at the top**: paste one or more YouTube, Apple Music or Spotify links (songs, albums, playlists), or the path to a Takeout export, and press Enter.
- **What ytm knows**: your music folder and format, how many songs you have (and from where), whether the Proton VPN and Apple Music are set up.
- **Everything it does, in one list**: download your YouTube Music library, upgrade your library to Apple Music, the YouTube Premium upgrade, fix tags, tidy file names, settings, a self-check, and updating yt-dlp/ffmpeg/deno. ↑↓ and Enter; Esc goes back from anywhere, and quits from the home screen.

**Download my YouTube Music library** looks through your Downloads, Desktop and Documents folders (including renamed folders like `~/Téléchargements`) for anything that contains YouTube Music data, whatever it is called, lists what it found with track counts, and shows a preview before it downloads. Exports split over several zips (`-001`, `-002`) are combined. You can also drag a zip, folder, `.csv` or text file of links into the terminal, or pass one on the command line. If you have no export yet, it walks you through getting one.

A library download also does these by default, each one switchable in Settings (or for one run with `--albums=false`, `--retry=false`, `--tag-after=false`, `--premium-after=false`): it completes every album you have a song from, retries songs that were unavailable before (including a YouTube Music search), gives the songs it just downloaded MusicBrainz tags when it's certain of them, and runs the Premium upgrade if you've set one up. Pasted links don't get their albums completed unless you add `--albums`.

On first run ytm offers a one-minute setup (music folder, Proton sign-in), fetches the tools it needs (yt-dlp, ffmpeg and a JavaScript runtime) and downloads with embedded cover art. Music goes to `~/Music` unless you choose otherwise in Settings or with `-o`.

Always put links in quotes on the command line: `&` and `?` mean things to your shell. (In ytm's own box they don't need any.)

## Install

| Platform | Command |
|---|---|
| Linux / macOS | `curl -fsSL https://raw.githubusercontent.com/bybrooklyn/ytm-to-mp3/main/install.sh \| sh` |
| Windows (PowerShell) | `irm https://raw.githubusercontent.com/bybrooklyn/ytm-to-mp3/main/install.ps1 \| iex` |
| Anything | download a binary from the [releases page](https://github.com/bybrooklyn/ytm-to-mp3/releases) (`.deb`/`.rpm` too) |
| From source | `just install` (or `go install github.com/bybrooklyn/ytm-to-mp3/cmd/ytm@latest`) |

## Getting your export

Pick **Download my YouTube Music library**, then "I don't have one yet". It opens [Google Takeout](https://takeout.google.com/), tells you exactly which boxes to tick (YouTube and YouTube Music → *music (library and uploads)*), waits for the zip to land in your Downloads folder and starts downloading by itself. Google can't be automated, so the clicking is yours.

## From the command line

`ytm <link or file>...` downloads without opening the home screen: YouTube, Apple Music or Spotify links, a Takeout `.zip`/folder/`.csv`, or a text file of links, mixed as you like. Options for one run (your defaults live in Settings):

`-o DIR` (default `~/Music`), `-f mp3|m4a|opus|flac`, `-j N` max parallel downloads, `--no-vpn`, `--connections N` (VPN exits this run), `--dry-run`, `--retry`, `--albums` (also the rest of every album you have a song from; `--albums-min N` limits it to albums with N or more of your songs), `--all`, `--source youtube`, `--maybe`, `--premium-upgrade`, `-y`. `ytm --completion bash|zsh|fish|powershell` prints tab completion (e.g. `source <(ytm --completion bash)`); `ytm --version`, `ytm --help`. Flags can go before or after the links.

`--albums` looks each album up on YouTube Music once (the answers are kept in the music folder's `.ytm` database), only accepts an album that has one of your songs on it, skips songs you already have (including a saved music video of the same song), and shows how many songs and roughly how much space before it downloads. The new songs land in the same `Artist/Album/` folders. Settings are saved in your config folder (the Settings screen shows the path); delete that file to reset.

Re-running is always safe: finished tracks are skipped, temporary failures retry, and tracks YouTube says are unavailable are remembered (`--retry` tries them again). Before giving up on an unavailable track, ytm searches YouTube Music for the same artist and title: songs whose old video was taken down usually live on under a new one. Set `YTM_DEBUG=1` to get a per-stage timing breakdown at the end of a run. Files from older runs (named `... [id].ext`) are recognised too.

## How it stays fast without getting blocked

- A three-stage pipeline (resolve → fetch → encode) with separate worker pools, so slow YouTube lookups, network transfers and CPU-bound encoding all overlap.
- Audio is pulled in parallel ranged requests (about four per song), which avoids YouTube's per-connection throttling. If a stream can't be fetched directly from one exit it moves to another; failing that, yt-dlp downloads it.
- Concurrency is adaptive: it ramps up while requests succeed, halves and pauses with jittered backoff the moment YouTube pushes back (HTTP 429, "confirm you're not a bot"). `-j` only sets the ceiling.
- Songs are looked up with one request to YouTube's player API (about 0.2 s) instead of a yt-dlp process (about 2 s); anything that can't be handled that way still goes through yt-dlp. In testing, 120 songs through 6 VPN exits took 37 s this way, against 3-4 minutes with yt-dlp lookups. `YTM_RESOLVER=yt-dlp` switches it off.

## Quality and files

- The default format is **opus**: YouTube's own best audio (Opus, about 130-160 kbps), copied into the file untouched, with tags and square cover art. m4a copies YouTube's AAC stream the same way; mp3 and flac are re-encoded from it.
- Files are named `Artist/Album/01 - Title.opus`: the track number (when known: from the album on YouTube Music, or YouTube's metadata) keeps albums in order. Two songs with the same title get `Title (2)`. The video link is in each file's comment tag, which is how ytm recognises its files even if its database is lost.
- **Tidy file names** on the home screen renames an existing folder to this scheme (removing the `[id]` older versions added), with a preview first; undo it from **Fix tags** → **Undo the last fix**.

## Google account: only for the optional Premium upgrade

Normal downloads never use your Google account or browser cookies. When YouTube blocks or throttles an exit, ytm drops that location, remembers it as blocked, and carries on from a different one.

If you have YouTube Premium you can optionally turn on the **Premium upgrade** (Settings, or **YouTube Premium upgrade** on the home screen): it re-downloads songs you already have at Premium quality (Opus or AAC at about 256 kbps), replacing each file in place. It is **experimental and uses your account**: YouTube can flag or ban accounts it thinks are bulk-downloading, so it goes slowly (one song at a time, 5-10 s apart, one VPN exit, at most 300 per run, `--premium-limit N`), never re-downloads a song at the same quality, and stops at the first bot check. YouTube doesn't always offer the Premium streams; songs without one are left alone. Use a spare Premium account if you can. ytm can import the sign-in straight from Firefox (and Zen, LibreWolf, Waterfox) or a Chromium-based browser (Chrome, Chromium, Brave, Vivaldi, Edge, Opera; on Linux the key comes from your KWallet or GNOME keyring, which may ask first), reading only youtube.com cookies. Or give it a `cookies.txt` exported from a private browser window that you then close (yt-dlp's advice; it lasts longer, because YouTube rotates the cookies of an open browser session). Chrome, Edge and Brave 127+ on Windows lock their cookies away from other apps: use a cookies.txt or Firefox there. ytm keeps its copy in its config folder, readable only by you.

## Apple Music (optional, your own subscription)

With an Apple Music subscription, ytm can download songs as **256 kbps AAC** (`.m4a`), decrypted locally with your account. Lossless (ALAC) is not available this way. Sign in from Settings (**Apple Music**): ytm finds a live music.apple.com session in Firefox or a Chromium-based browser, checks it with Apple and picks up your country's store. Once you're signed in, Apple Music and Spotify links come from Apple Music first, and **Upgrade my library to Apple Music** finds your YouTube library there.

- **Upgrade my library to Apple Music** looks every song up once (the answers are kept in the music folder's `.ytm` database), ignoring YouTube's upload noise ("Official Audio", "- Topic"). It refuses a different version: a live, remix, instrumental or sped-up recording doesn't count as the song. It shows how many it found, sure and less certain, and lists the less certain ones and the misses in `.ytm/apple-match.txt`. Then you choose: only the sure matches or all of them, and **next to** the YouTube copies (same name, `.m4a`) or **instead of** them (each YouTube file moves to `.ytm/replaced/`, and later library runs won't fetch it from YouTube again).
- Songs are named like the YouTube downloads (`Artist/Album/01 - Title.m4a`), with Apple's tags, cover art, genre and a link back in the comment tag. Several download at once over your VPN exits. A song that fails on the network is retried on another exit, and a song Apple doesn't offer your account is remembered and skipped. Catalog lookups are paced per exit, because Apple rate-limits quick searches.

## Links from any service: best source per song

Paste a YouTube, Apple Music or Spotify link (or several, mixed) into the box on the home screen, or give them to `ytm <link>...`; the VPN connects once for the whole run.

- **Spotify** gives only the song list (its audio is DRM'd and never downloaded); it needs no login, and works for public albums, playlists and tracks.
- **Apple Music and Spotify songs** come from Apple Music (256 kbps AAC) when you're signed in there and it has the song (an Apple link's own songs need no search), otherwise from YouTube Music by search. Without an Apple sign-in, Apple links still work: they come from YouTube. `--source youtube` always uses YouTube; `--maybe` also takes less certain Apple matches.
- **No duplicates:** a song you already have, however it was downloaded (your YouTube library, the Apple Music upgrade, an earlier link), is skipped. The same album pasted from two services is one set of songs. A song you have from YouTube that Apple Music now gives is upgraded; the YouTube file moves to `.ytm/replaced/` and library runs won't fetch it again.

## Metadata

Everything YouTube knows is kept: title, artist(s), album, release or upload date, genre, track number, a link back to the video, and square cover art. The CSV from Google takes priority over what YouTube reports.

**Fix tags** (home screen) improves files you already have, and it is deliberately cautious:

- Removes obvious upload noise (`(Official Video)`, `[HD]`, `- Topic`, `Artist - ` title prefixes). Never touches `(Live)`, `(Remastered)` and similar.
- Asks [MusicBrainz](https://musicbrainz.org) about each song and **only acts when it is essentially certain**: same title, same artist, length within 2 seconds, top search score and no rival recording. Then it adds the MusicBrainz IDs and ISRC, fixes spelling, and fills album, date, track number and cover art only when the album is unambiguous.
- Anything less certain is left exactly as YouTube tagged it.
- It shows what it would change first and writes only when you say so; **Undo the last fix** restores the previous tags, cover art and file names. It can also move files into `Artist/Album/01 - Title.ext`, and work offline (no MusicBrainz).

## Good to know

- Songs you uploaded yourself (not public on YouTube) usually can't be downloaded.
- Windows and macOS builds are compiled and unit-tested in CI; most real-world testing so far has been on Linux. Bug reports welcome.
- Only download music you have the right to. This tool automates what yt-dlp does; respect YouTube's terms and copyright law where you live.

MIT licensed.

## Downloading anything from YouTube

Any video, playlist or album URL works, and you can pass several at once:

```
ytm "https://music.youtube.com/playlist?list=..." "https://youtu.be/dQw4w9WgXcQ" -o ~/Music
```

## Proton VPN (optional, much faster)

Sign in once in Settings (**Proton account**); from then on every download goes through several VPN servers at once. **Test VPN** in Settings shows the servers in use and the IP each one exits from; `--no-vpn` skips the VPN for one run.

Signing in uses your Proton account (the password goes only to Proton and is never saved; 2FA codes are supported) and registers WireGuard keys for ytm under your account, one per connection (Proton routes a key through only one server at a time, so connections can't share one). Every run connects 3 servers near you (change the number in settings; "Auto" uses as many as your plan allows, minus one kept as a spare, though in testing 10 were no faster than 6; distance matters, since every lookup is several round trips through the tunnel) and spreads tracks over them; a server that gets rate-limited rests, and after repeated trouble ytm swaps it for a fresh server.

The tunnels are userspace WireGuard inside ytm itself: no root, no WireGuard install, no system changes, and the same on Linux, macOS and Windows. If the VPN is on but can't connect, ytm stops rather than falling back to your own IP. Needs single-password mode on your Proton account and a plan that includes VPN.

## Development

`just` lists the tasks: `just build`, `just install`, `just check`, `just get <url>`.
