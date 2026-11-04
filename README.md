# ytm

Download your YouTube Music library as properly tagged audio files. One small binary for Windows, macOS and Linux.

```
ytm setup                          # once: Proton sign-in (optional), music folder, format
ytm "https://youtu.be/..."         # any video, playlist or album link (keep the quotes)
ytm                                # a short menu: import your library, download a link, fix tags, settings
```

Plain `ytm` in a terminal opens a menu. **Import my YouTube Music library** looks through your Downloads, Desktop and Documents folders (including renamed folders like `~/Téléchargements`) for anything that contains YouTube Music data, whatever it is called, lists what it found with track counts, and shows a preview before it downloads. Exports split over several zips (`-001`, `-002`) are combined. You can also drag a zip, folder, `.csv` or text file of links into the terminal when it asks "Where is it?", or pass one on the command line. Before downloading it offers options: retry songs that were unavailable last time, also get the rest of every album you have a song from, and fix tags with MusicBrainz afterwards. **Fix tags in my music folder** runs `ytm tag` with a preview you can then apply, and **Update yt-dlp, ffmpeg and deno** refreshes the tools.

On first run ytm fetches the tools it needs (yt-dlp, ffmpeg and a JavaScript runtime) and downloads with embedded cover art. Music goes to `~/Music` unless you choose otherwise in `ytm setup` or with `-o`.

Always put links in quotes: `&` and `?` mean things to your shell.

## Install

| Platform | Command |
|---|---|
| Linux / macOS | `curl -fsSL https://raw.githubusercontent.com/bybrooklyn/ytm-to-mp3/main/install.sh \| sh` |
| Windows (PowerShell) | `irm https://raw.githubusercontent.com/bybrooklyn/ytm-to-mp3/main/install.ps1 \| iex` |
| Anything | download a binary from the [releases page](https://github.com/bybrooklyn/ytm-to-mp3/releases) (`.deb`/`.rpm` too) |
| From source | `just install` (or `go install github.com/bybrooklyn/ytm-to-mp3/cmd/ytm@latest`) |

## Getting your export

Run `ytm export`. It opens [Google Takeout](https://takeout.google.com/), tells you exactly which boxes to tick (YouTube and YouTube Music → *music (library and uploads)*), waits for the zip to land in your Downloads folder and starts downloading by itself. Google can't be automated, so the clicking is yours.

## Commands

```
ytm setup                    one-time setup (Proton sign-in, music folder, format)
ytm <input>...               download video/playlist links, a Takeout .zip/folder/.csv, or a text file of links
ytm status                   show settings and test the VPN exits
ytm doctor                   check tools, network, folders and login, and say how to fix problems
ytm completion SHELL         tab completion: bash, zsh, fish or powershell (e.g. `source <(ytm completion bash)`)
ytm logout                   sign out of Proton and delete ytm's saved login
ytm export                   guide me through getting the export
ytm tag <folder>             improve metadata (preview; add --apply to write, --undo to revert)
ytm update                   refresh yt-dlp
ytm version                  (or --version)
```

Options: `-o DIR` (default `~/Music`), `-f mp3|m4a|opus|flac`, `-j N` max parallel downloads, `--no-vpn`, `--connections N` (VPN exits this run), `--dry-run`, `--retry`, `--albums` (also the rest of every album you have a song from; `--albums-min N` limits it to albums with N or more of your songs), `--all`. Flags can go before or after the command.

`--albums` looks each album up on YouTube Music once (the answers are kept in the music folder's `.ytm` database), only accepts an album that has one of your songs on it, skips songs you already have (including a saved music video of the same song), and shows how many songs and roughly how much space before it downloads. The new songs land in the same `Artist/Album/` folders. Settings are saved in your config folder (`ytm status` prints the path); delete that file to reset.

Re-running is always safe: finished tracks are skipped, temporary failures retry, and tracks YouTube says are unavailable are remembered (`--retry` tries them again). Before giving up on an unavailable track, ytm searches YouTube Music for the same artist and title: songs whose old video was taken down usually live on under a new one. Set `YTM_DEBUG=1` to get a per-stage timing breakdown at the end of a run. Files from older runs (named `... [id].ext`) are recognised too.

## How it stays fast without getting blocked

- A three-stage pipeline (resolve → fetch → encode) with separate worker pools, so slow YouTube lookups, network transfers and CPU-bound encoding all overlap.
- Audio is pulled in parallel ranged requests (about four per song), which avoids YouTube's per-connection throttling. If a stream can't be fetched directly from one exit it moves to another; failing that, yt-dlp downloads it.
- Concurrency is adaptive: it ramps up while requests succeed, halves and pauses with jittered backoff the moment YouTube pushes back (HTTP 429, "confirm you're not a bot"). `-j` only sets the ceiling.
- Experimental: `YTM_RESOLVER=go` looks songs up with one request to YouTube's player API instead of a yt-dlp process (about 0.2 s instead of 2 s); anything it can't handle still goes through yt-dlp.

## Quality and files

- The default format is **opus**: YouTube's own best audio (Opus, about 130-160 kbps), copied into the file untouched, with tags and square cover art. m4a copies YouTube's AAC stream the same way; mp3 and flac are re-encoded from it.
- Files are named `Artist/Album/01 - Title.opus`: the track number (when known: from the album on YouTube Music, or YouTube's metadata) keeps albums in order. Two songs with the same title get `Title (2)`. The video link is in each file's comment tag, which is how ytm recognises its files even if its database is lost.
- **Tidy file names** in the menu renames an existing folder to this scheme (removing the `[id]` older versions added), with a preview first; undo it from **Fix tags** → **Undo the last fix**.

## Google account: only for the optional Premium upgrade

Normal downloads never use your Google account or browser cookies. When YouTube blocks or throttles an exit, ytm drops that location, remembers it as blocked, and carries on from a different one.

If you have YouTube Premium you can optionally turn on the **Premium upgrade** (settings, or the menu): it re-downloads songs you already have at Premium quality (Opus or AAC at about 256 kbps), replacing each file in place. It is **experimental and uses your account**: YouTube can flag or ban accounts it thinks are bulk-downloading, so it goes slowly (one song at a time, 5-10 s apart, one VPN exit, at most 300 per run, `--premium-limit N`), never re-downloads a song at the same quality, and stops at the first bot check. YouTube doesn't always offer the Premium streams; songs without one are left alone. Use a spare Premium account if you can. Export the cookies from a private browser window and close it afterwards (yt-dlp's advice); ytm keeps its copy in its config folder, readable only by you.

## Metadata

Everything YouTube knows is kept: title, artist(s), album, release or upload date, genre, track number, a link back to the video, and square cover art. The CSV from Google takes priority over what YouTube reports.

`ytm tag` improves files you already have, and it is deliberately cautious:

- Removes obvious upload noise (`(Official Video)`, `[HD]`, `- Topic`, `Artist - ` title prefixes). Never touches `(Live)`, `(Remastered)` and similar.
- Asks [MusicBrainz](https://musicbrainz.org) about each song and **only acts when it is essentially certain**: same title, same artist, length within 2 seconds, top search score and no rival recording. Then it adds the MusicBrainz IDs and ISRC, fixes spelling, and fills album, date, track number and cover art only when the album is unambiguous.
- Anything less certain is left exactly as YouTube tagged it.
- It previews by default, `--apply` writes, `--undo` restores the previous tags, cover art and file names. `--rename` also moves files into `Artist/Album/01 - Title.ext`; `--offline` skips MusicBrainz.

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

```
ytm setup      # sign in to Proton, pick your music folder and format. Once.
ytm <link>     # that's it; downloads now go through several VPN servers at once
ytm status     # shows the servers in use and the IP each one exits from
ytm --no-vpn <link>   # skip the VPN for one run
ytm logout
```

`ytm setup` is a short inline form. It signs in with your Proton account (the password goes only to Proton and is never saved; 2FA codes are supported) and registers WireGuard keys for ytm under your account, one per connection (Proton routes a key through only one server at a time, so connections can't share one). Every run connects 3 servers near you (change the number in settings; "Auto" uses as many as your plan allows, minus one kept as a spare, though in testing 10 were no faster than 6; distance matters, since every lookup is several round trips through the tunnel) and spreads tracks over them; a server that gets rate-limited rests, and after repeated trouble ytm swaps it for a fresh server.

The tunnels are userspace WireGuard inside ytm itself: no root, no WireGuard install, no system changes, and the same on Linux, macOS and Windows. If the VPN is on but can't connect, ytm stops rather than falling back to your own IP. Needs single-password mode on your Proton account and a plan that includes VPN.

## Development

`just` lists the tasks: `just build`, `just install`, `just check`, `just get <url>`.
