# mfetchy

mfetchy downloads your music library from YouTube Music, Apple Music and Spotify as properly tagged audio files. Run `mfy`. One small binary for Windows, macOS and Linux.

```
mfy                                # a short menu: your library, links, Apple Music, settings, tools
mfy "https://youtu.be/..."         # or download a link straight away (keep the quotes)
```

There are no subcommands to remember: everything is in the menu that plain `mfy` opens in a terminal. It is short: **Download my YouTube Music library**, **Download a link** (YouTube, Apple Music, Spotify, SoundCloud or Bandcamp), **Apple Music...** (sign in, find your library there, check or remove the sign-in), **Settings**, and **More tools...** (fix tags, tidy file names, the Premium upgrade, updating yt-dlp/ffmpeg/deno, a self-check). Esc goes back a step; Ctrl-C quits.

**Download my YouTube Music library** can read your library straight from a browser that is signed in to YouTube Music, with no export at all: pick **Read it from my signed-in browser** and mfetchy reads that browser's youtube.com sign-in once, in memory (only the browser's name is remembered), and lists your Liked music, songs saved to your library, saved albums and playlists, from your own connection (not the VPN). Otherwise it looks through your Downloads, Desktop and Documents folders (including renamed folders like `~/Téléchargements`) for anything that contains YouTube Music data, whatever it is called, lists what it found with track counts, and shows a preview before it downloads. Exports split over several zips (`-001`, `-002`) are combined. You can also drag a zip, folder, `.csv` or text file of links into the terminal, or pass one on the command line. If you have no export yet, it walks you through getting one.

Albums come whole by default: a pasted song link (YouTube, Apple Music or Spotify) downloads the album it is from, numbered, and a library or playlist download (YouTube, Apple Music or Spotify) offers the rest of every album it has a song from (after showing how many songs and how much space). Spotify doesn't say which album a song is on, so mfetchy finds that in Apple Music's catalog (no sign-in needed), else on YouTube Music. `--song` gets just the songs given for one run; the "Complete albums" setting turns it off for good. A download also does these by default, each one switchable in Settings (or for one run with `--albums=false`, `--retry=false`, `--tag-after=false`, `--premium-after=false`): it retries songs that were unavailable before (including a YouTube Music search), gives the songs it just downloaded MusicBrainz tags when it's certain of them, and runs the Premium upgrade if you've set one up.

Songs are tagged with everything known about them: title, artists (featured ones included), album, album artist, track and disc numbers with totals, release date, genre, and square cover art up to 1200 px (Opus included): the album's own art from YouTube Music, Apple Music or Spotify where there is one, the video's thumbnail otherwise. Songs that come via Spotify or Apple Music links keep that catalog's release date, album art and ISRC; Apple Music songs also get the composer and copyright. The MusicBrainz pass writes the standard MusicBrainz IDs and ISRC that players and Picard read (m4a can only hold the common tags), and only replaces cover art that is missing, small or not square.

On first run mfetchy offers a one-minute setup (music folder, Proton sign-in), fetches the tools it needs (yt-dlp, ffmpeg and a JavaScript runtime) and downloads with embedded cover art. Music goes to `~/Music` unless you choose otherwise in Settings or with `-o`.

Always put links in quotes on the command line: `&` and `?` mean things to your shell. (In mfetchy's own box they don't need any.)

## Install

| Platform | Command |
|---|---|
| Linux / macOS | `curl -fsSL https://raw.githubusercontent.com/bybrooklyn/mfetchy/main/install.sh \| sh` |
| Windows (PowerShell) | `irm https://raw.githubusercontent.com/bybrooklyn/mfetchy/main/install.ps1 \| iex` |
| Anything | download a binary from the [releases page](https://github.com/bybrooklyn/mfetchy/releases) (`.deb`/`.rpm` too) |
| From source | `just install` (or `go install github.com/bybrooklyn/mfetchy/cmd/mfy@latest`) |

## Getting your export

Pick **Download my YouTube Music library**, then "I don't have one yet". It opens [Google Takeout](https://takeout.google.com/), tells you exactly which boxes to tick (YouTube and YouTube Music → *music (library and uploads)*), waits for the zip to land in your Downloads folder and starts downloading by itself. Google can't be automated, so the clicking is yours.

## From the command line

`mfy <link or file>...` downloads without the menu: YouTube, Apple Music, Spotify, SoundCloud or Bandcamp links, a Takeout `.zip`/folder/`.csv`, or a text file of links, mixed as you like. Options for one run (your defaults live in Settings):

`-o DIR` (default `~/Music`), `-f mp3|m4a|opus|flac`, `-j N` max parallel downloads, `--no-vpn`, `--connections N` (VPN exits this run), `--dry-run`, `--retry`, `--song` (just the songs given, no albums; `--albums-min N` limits album completion to albums with N or more of your songs), `--all`, `--source youtube`, `--maybe`, `--premium-upgrade`, `-y`. `mfy --completion bash|zsh|fish|powershell` prints tab completion (e.g. `source <(mfy --completion bash)`); `mfy --version`, `mfy --help`. Flags can go before or after the links.

Album completion looks each album up on YouTube Music once (the answers are kept in the music folder's `.mfetchy` database), only accepts an album that has one of your songs on it, skips songs you already have (including a saved music video of the same song), and shows how many songs and roughly how much space before it downloads. The new songs land in the same `Artist/Album/` folders. Settings are saved in your config folder (the Settings screen shows the path); delete that file to reset.

Re-running is always safe: finished tracks are skipped, temporary failures retry, and tracks YouTube says are unavailable are remembered (`--retry` tries them again). Before giving up on an unavailable track, mfetchy searches YouTube Music for the same artist and title: songs whose old video was taken down usually live on under a new one. Set `MFY_DEBUG=1` to get a per-stage timing breakdown at the end of a run. Files from older runs (named `... [id].ext`) are recognised too.

## How it stays fast without getting blocked

- A three-stage pipeline (resolve → fetch → encode) with separate worker pools, so slow YouTube lookups, network transfers and CPU-bound encoding all overlap.
- Audio is pulled in parallel ranged requests (about four per song), which avoids YouTube's per-connection throttling. If a stream can't be fetched directly from one exit it moves to another; failing that, yt-dlp downloads it.
- Concurrency is adaptive: it ramps up while requests succeed, halves and pauses with jittered backoff the moment YouTube pushes back (HTTP 429, "confirm you're not a bot"). `-j` only sets the ceiling.
- Songs are looked up with one request to YouTube's player API (about 0.2 s) instead of a yt-dlp process (about 2 s); anything that can't be handled that way still goes through yt-dlp. In testing, 120 songs through 6 VPN exits took 37 s this way, against 3-4 minutes with yt-dlp lookups. `MFY_RESOLVER=yt-dlp` switches it off.

## What runs in Go, and what doesn't

mfetchy itself is Go. Searching YouTube Music, listing albums and playlists, reading your library, and looking songs up and fetching their audio are done in Go, with no other program. Audio files themselves are never parsed or rewritten by mfetchy: **ffmpeg** and **ffprobe** do that (remuxing, tags, cover art, mp3 and flac), and **yt-dlp** is the fallback for videos mfetchy's own client can't open, the Premium upgrade, and SoundCloud and Bandcamp. mfetchy fetches whatever is missing itself, and skips a copy on your PATH that doesn't run. `MFY_SEARCH=yt-dlp` and `MFY_RESOLVER=yt-dlp` switch the Go lookups off. The menu's **More tools** > self-check shows what is installed.

## Quality and files

- The default format is **opus**: YouTube's own best audio (Opus, about 130-160 kbps), copied into the file untouched, with tags and square cover art. m4a copies YouTube's AAC stream the same way; mp3 and flac are re-encoded from it.
- Files are named `Artist/Album/01 - Title.opus`: the track number (when known: from the album on YouTube Music, or YouTube's metadata) keeps albums in order. Two songs with the same title get `Title (2)`. The video link is in each file's comment tag, which is how mfetchy recognises its files even if its database is lost.
- **Tidy file names** (More tools) renames an existing folder to this scheme (removing the `[id]` older versions added), with a preview first; undo it from **Fix tags** → **Undo the last fix**.

## Album art and .nfo files

For Jellyfin, Plex, Kodi and other players, mfetchy writes `folder.jpg` (the album's cover, square, up to 1200 px: the largest square cover among the album's songs), `album.nfo` in each album folder and `artist.nfo` in each artist folder (Kodi's music NFO format, which Jellyfin reads too: titles, artist, year, genres, label, MusicBrainz IDs, and the tracks with position, length and IDs). They are written once per album after a download, and **More tools** > **Write cover and .nfo files for my library** does it for what you already have (with a preview first). `folder.jpg` and the `.nfo` files are on by default and `cover.png` (same art as PNG) is off; each has a row in Settings. Files mfetchy writes carry a "written by mfetchy" mark and are refreshed only when something changed; a file of the same name that you made yourself is never touched. Merging duplicate folders carries these files along.

## Google account: only for the optional Premium upgrade

Normal downloads never use your Google account or browser cookies. When YouTube blocks or throttles an exit, mfetchy drops that location, remembers it as blocked, and carries on from a different one.

If you have YouTube Premium you can optionally turn on the **Premium upgrade** (Settings, or More tools): it re-downloads songs you already have at Premium quality (Opus or AAC at about 256 kbps), replacing each file in place. It is **experimental and uses your account**: YouTube can flag or ban accounts it thinks are bulk-downloading, so it goes slowly (one song at a time, 5-10 s apart, one VPN exit, at most 300 per run, `--premium-limit N`), never re-downloads a song at the same quality, and stops at the first bot check. YouTube doesn't always offer the Premium streams; songs without one are left alone. Use a spare Premium account if you can. mfetchy can import the sign-in straight from Firefox (and Zen, LibreWolf, Waterfox) or a Chromium-based browser (Chrome, Chromium, Brave, Vivaldi, Edge, Opera; on Linux the key comes from your KWallet or GNOME keyring, which may ask first), reading only youtube.com cookies. Or give it a `cookies.txt` exported from a private browser window that you then close (yt-dlp's advice; it lasts longer, because YouTube rotates the cookies of an open browser session). Chrome, Edge and Brave 127+ on Windows lock their cookies away from other apps: use a cookies.txt or Firefox there. mfetchy keeps its copy in its config folder, readable only by you.

## Apple Music (optional, your own subscription)

With an Apple Music subscription, mfetchy can download songs as **256 kbps AAC** (`.m4a`), decrypted locally with your account. Lossless (ALAC) is not available this way. Sign in from **Apple Music...** in the menu, or Settings: mfetchy finds a live music.apple.com session in Firefox or a Chromium-based browser, checks it with Apple and picks up your country's store. Once you're signed in, Apple Music and Spotify links come from Apple Music first, and **Apple Music...** → **Find my YouTube library on Apple Music** gets your library from there.

- **Find my YouTube library on Apple Music** looks every song up once (the answers are kept in the music folder's `.mfetchy` database), ignoring YouTube's upload noise ("Official Audio", "- Topic"). It refuses a different version: a live, remix, instrumental or sped-up recording doesn't count as the song. It shows how many it found, sure and less certain, and lists the less certain ones and the misses in `.mfetchy/apple-match.txt`. Then you choose: only the sure matches or all of them, and **next to** the YouTube copies (same name, `.m4a`) or **instead of** them (each YouTube file moves to `.mfetchy/replaced/`, and later library runs won't fetch it from YouTube again).
- Songs are named like the YouTube downloads (`Artist/Album/01 - Title.m4a`), with Apple's tags, cover art, genre and a link back in the comment tag. Several download at once over your VPN exits. A song that fails on the network is retried on another exit, and a song Apple doesn't offer your account is remembered and skipped. Catalog lookups are paced per exit, because Apple rate-limits quick searches.

## Tidal (optional, your own subscription)

With a Tidal HiFi subscription, mfetchy can download songs as **lossless FLAC** (16-bit/44.1 kHz). **Tidal's terms do not allow downloading its music.** This is meant for songs you can already stream with your own paid account, kept for your own use (do not share the files); Tidal can limit or close an account it thinks is downloading, so go gently and use a spare account if you can. Tidal has no public download API, and this can stop working whenever Tidal changes things. mfetchy shows this warning and asks you to accept it before every sign-in.

- **Sign in** under **Connections** > **Tidal** (or Settings). mfetchy shows a code and a link; open the link in any browser, approve it, and mfetchy picks it up. It never reads your browser or your password. The login is kept in `tidal.json` in mfetchy's config folder, readable only by you.
- **Only plain streams.** mfetchy fetches a stream only when Tidal serves it as an ordinary FLAC file. A stream served encrypted or as a DASH manifest is skipped, never decrypted, and that song comes from Apple Music or YouTube instead; the output says which quality you got. **Tidal quality** in Settings: *lossless* (the default) or *best plain tier* (tries hi-res first and takes it only if it is a plain file).
- **Links:** Tidal track, album and playlist links work in **Download a link** and on the command line. When you're signed in, songs from any link or library are looked up on Tidal (by ISRC, then by name and length), and Tidal wins over Apple Music: the order is YouTube < Apple Music AAC < Tidal FLAC < a Bandcamp purchase. A song you hold from a lower source is upgraded (the old file moves to `.mfetchy/replaced/`). Files are `Artist/Album/01 - Title.flac`, tagged with ISRC, track and disc numbers, release date and cover.
- Songs download one at a time, from your own connection (not the VPN). `mfy doctor` checks the login.

## Links from any service: best source per song

Paste a YouTube, Apple Music, Spotify or Tidal link (or several, mixed) into **Download a link**, or give them to `mfy <link>...`; the VPN connects once for the whole run.

- **Spotify** gives only the song list (its audio is DRM'd and never downloaded); it needs no login, and works for public albums, playlists and tracks.
- **Apple Music and Spotify songs** come from Apple Music (256 kbps AAC) when you're signed in there and it has the song (an Apple link's own songs need no search), otherwise from YouTube Music by search. Without an Apple sign-in, Apple links still work: they come from YouTube. `--source youtube` always uses YouTube; `--maybe` also takes less certain Apple matches.
- **No duplicates:** a song you already have, however it was downloaded (your YouTube library, Apple Music, an earlier link), is skipped. The same album pasted from two services is one set of songs. A song you have from YouTube that Apple Music now gives is upgraded; the YouTube file moves to `.mfetchy/replaced/` and library runs won't fetch it again.

## SoundCloud and Bandcamp

Paste a SoundCloud or Bandcamp link (a song, an album or playlist, or an artist's page) into **Download a link**, or give it to `mfy <link>`. SoundCloud links are read by mfetchy itself (its own client for SoundCloud's public API, identifying itself as `mfy`, public tracks only, no sign-in), with yt-dlp as the automatic fallback when that fails (`MFY_SOUNDCLOUD=yt-dlp` forces yt-dlp); yt-dlp finds the songs on Bandcamp (it shows non-browser programs a bot check that only yt-dlp keeps up with); everything else is mfetchy: songs you already have are skipped (whichever service they came from), and each file is tagged with the album, track number and date the site gives, square cover art up to 1200 px, and filed as `Artist/Album/01 - Title`.

**Music you have bought on Bandcamp** can come in its original quality instead of the stream: under **Connections** > **Bandcamp**, pick the browser that is signed in to bandcamp.com (only the browser's name is remembered). From then on a Bandcamp link to something you own downloads the best original offered (FLAC, else 320 kbps MP3), exactly as Bandcamp serves it, and a lower-quality copy you already have of the same song is moved to `.mfetchy/replaced`. Paste your own profile link (`https://bandcamp.com/yourname`) to get your whole collection. Anything you haven't bought still streams. The sign-in is read from the browser each time, kept in memory, and only ever sent to Bandcamp.

Otherwise they serve music at stream quality (Bandcamp 128 kbps MP3, SoundCloud 128 kbps MP3 or AAC/Opus), so files keep the format the site serves rather than being re-encoded. These downloads go out from your own connection, not the VPN. SoundCloud's terms don't allow copying tracks whose uploader hasn't enabled downloads: the same applies here as to any yt-dlp use, so only download what you have the right to.

## All my accounts at once

**Download from all my accounts** (main menu) reads the library of every account you have set up, merges them into one list and downloads what your music folder is missing, each song from the best source any of your accounts can give: a Bandcamp purchase's original files, then Apple Music (256 kbps AAC), then YouTube. First it shows which accounts it will use (YouTube Music and Bandcamp via the browser chosen under **Connections**, Apple Music via your Apple sign-in; accounts not set up are skipped with a hint), asks before reading, then previews: songs per account, how many are already held, how many are new, how many would be upgrades (say, a YouTube copy of an album you bought on Bandcamp) and a rough size.

- **One song, many accounts:** songs match by ISRC when both sides have one, otherwise by title, artist and album (editions like "Deluxe" are one album, "feat." credits don't split a song). The merged song keeps the best of each field (the same rules as tag merging). A Bandcamp album purchase supplies every song of that album found in your other accounts; Bandcamp doesn't list an album's tracks, so one you haven't downloaded yet is fetched as the album.
- **Apple Music** reads your library's songs that have an Apple Music version (songs you uploaded yourself are left out).
- **What it sends:** each account is read only from its own service (YouTube Music from music.youtube.com, Bandcamp from bandcamp.com, Apple from amp-api.music.apple.com), from your own connection, not the VPN. Browser sign-ins are read once, kept in memory and never saved or sent elsewhere; nothing is read for an account you haven't set up. Downloads then work as everywhere else in mfetchy.
- Running it again right after plans nothing new.

## Metadata

Everything YouTube knows is kept: title, artist(s), album, release or upload date, genre, track number, a link back to the video, and square cover art. The CSV from Google takes priority over what YouTube reports.

**Fix tags** (More tools) improves files you already have, and it is deliberately cautious:

- Removes obvious upload noise (`(Official Video)`, `[HD]`, `- Topic`, `Artist - ` title prefixes). Never touches `(Live)`, `(Remastered)` and similar.
- Asks [MusicBrainz](https://musicbrainz.org) about each song and **only acts when it is essentially certain**: same title, same artist, length within 2 seconds, top search score and no rival recording. Then it adds the MusicBrainz IDs and ISRC, fixes spelling, and fills album, date, track number and cover art only when the album is unambiguous.
- Anything less certain is left exactly as YouTube tagged it.
- It shows what it would change first and writes only when you say so; **Undo the last fix** restores the previous tags, cover art and file names. It can also move files into `Artist/Album/01 - Title.ext`, and work offline (no MusicBrainz).

## Lyrics

New songs get lyrics after they download (a "Fetch lyrics after" row in Settings, on by default), and **Fetch lyrics for my library** (More tools) does the songs you already have. It prints a line per song, can be stopped and run again (finished songs are skipped), and asks two songs at a time with a short pause between requests.

- **Line-synced whenever a source has it.** Synced lyrics are stored as LRC text (`[01:23.45]line`, with `[ti:]`, `[ar:]`, `[al:]`, `[length:]` and a `[by:]` source credit) in a `.lrc` file next to the song with the same name (what most players, Navidrome and Jellyfin read) **and** in the file's lyrics tag. Plain lyrics, when that is all there is, go in a `.txt` beside the song and the same tag. Opus, Ogg and FLAC keep this in a Vorbis comment `LYRICS`; MP3 in an ID3 unsynchronised-lyrics frame; M4A in `©lyr`. A player that does not read LRC from the tag shows it as text; one that reads the sidecar shows it synced.
- **Careful matching.** Title and artist have to match (ignoring `feat.`, `(Remastered)`, `- Topic`), a different version is refused (a live take does not get the studio version's timing, nor a remix), and synced lyrics are used only when their length is within 3 seconds of your file's, so the lines do not drift. Otherwise the words alone are used when the length is within 15 seconds, else nothing. Songs that are only instrumental get no file.
- **Sources.** [LRCLIB](https://lrclib.net) (free, community, on by default). NetEase Cloud Music is an unofficial, undocumented service in China: it is off until you switch it on in Settings, and is only asked if LRCLIB has nothing. Each is a row in Settings.
- **Privacy.** A source is told a song's title, artist, album and length, nothing else: no file path, no account, no library list. Lookups go straight from your computer (not through the VPN) with a plain `mfetchy` user agent.
- **Cached.** Everything found is kept in `.mfetchy/lyrics` in your music folder, keyed by the song (not by file name), so a song is never fetched twice, and a renamed file or one upgraded to a better source (Apple Music over YouTube) takes its lyrics along. "No lyrics found" is remembered for 14 days before it is asked again. Lyrics already in a file, or a `.lrc` you put there yourself, are never replaced.

## Good to know

- Songs you uploaded yourself (not public on YouTube) usually can't be downloaded.
- Windows and macOS builds are compiled and unit-tested in CI; most real-world testing so far has been on Linux. Bug reports welcome.
- Only download music you have the right to. This tool automates what yt-dlp does; respect YouTube's terms and copyright law where you live.

MIT licensed.

## Downloading anything from YouTube

Any video, playlist or album URL works, and you can pass several at once:

```
mfy "https://music.youtube.com/playlist?list=..." "https://youtu.be/dQw4w9WgXcQ" -o ~/Music
```

## VPN (optional, much faster)

Proton VPN, Mullvad or your own WireGuard config: set one up in Settings (**VPN account**). Mullvad needs only your account number; a WireGuard config is any standard `wg-quick` `.conf` (IVPN, Windscribe, AirVPN, your own server...; one file is one connection). What mfetchy sends where, and the private mode that turns off optional lookups, is in [PRIVACY.md](PRIVACY.md). The details below are for Proton; with Mullvad, mfetchy registers one key per connection under your account (it allows 5 devices, so at most the free ones are used, and signing out removes them).

Sign in once in Settings (**VPN account**); from then on every download goes through several VPN servers at once. **Test VPN** in Settings shows the servers in use and the IP each one exits from; `--no-vpn` skips the VPN for one run.

Signing in uses your Proton account (the password goes only to Proton and is never saved; 2FA codes are supported) and registers WireGuard keys for mfetchy under your account, one per connection (Proton routes a key through only one server at a time, so connections can't share one). Every run connects 3 servers near you (change the number in settings; "Auto" uses as many as your plan allows, minus one kept as a spare, though in testing 10 were no faster than 6; distance matters, since every lookup is several round trips through the tunnel) and spreads tracks over them. The connecting starts the moment a run begins, alongside the yt-dlp/ffmpeg check and reading your export, and the run goes on as soon as the first server is up (links are looked up through it, several at once); the others join a few seconds later. A server that gets rate-limited rests, and after repeated trouble mfetchy swaps it for a fresh server.

The tunnels are userspace WireGuard inside mfetchy itself: no root, no WireGuard install, no system changes, and the same on Linux, macOS and Windows. If the VPN is on but can't connect, mfetchy stops rather than falling back to your own IP. Needs single-password mode on your Proton account and a plan that includes VPN.

## Desktop app (early)

**mFetchy Desktop** (`mfy-desktop`) is a native window (built with [Fyne](https://fyne.io), not a web page) that shows mfetchy's screens instead of a terminal. The screens are defined once, in `internal/screen`, as plain descriptions of what to show and how keys change it; the terminal UI and the window each just draw that description, so they behave the same: same rows, same keys (arrows, vi keys, Space, Enter), plus the mouse and Ctrl+1/2 for the sidebar. The window draws them as a desktop app (sidebar, sections, switches, dropdowns, steppers, a folder picker; light and dark following the system) rather than as terminal text; the design rules are in [docs/DESIGN.md](docs/DESIGN.md). It does everything without a terminal: **Downloads** (paste or drop links and files, live progress per song), **Library** (download from all your accounts, your YouTube Music library read from a browser, Bandcamp purchases, a Takeout export, finding your YouTube library on Apple Music), **Tools** (check that everything works, test the VPN, update the download tools, fix tags, tidy names and merge duplicate folders, lyrics, cover and `.nfo` files, each with its output shown and a Cancel button), and **Connections** and **Settings**, where every sign-in (Proton, Mullvad, your own WireGuard config, Apple Music, Tidal with its code and link, YouTube Premium, the browser for YouTube Music and Bandcamp) is a dialog in the window. These run the same code as the terminal; their questions are dialogs, and passwords and tokens are masked. Release builds for Linux, macOS and Windows are attached to each [release](https://github.com/bybrooklyn/mfetchy/releases) as `mfy-desktop_<version>_<os>_<arch>` with a checksum. To build it yourself, `just build-desktop` needs a C compiler (and on Linux the OpenGL and X11 development packages), which the `mfy` command itself does not.

## Coming from ytm

mfetchy was called ytm before. Nothing to do by hand: on first run it renames your old data folders in place (settings and sign-ins in the user config folder, the managed tools in the user cache folder, and `.ytm` inside each music folder, with its database, backups and caches, to `.mfetchy`). It never copies or overwrites; if a rename can't be done it keeps using the old folder and says so. Files the old version wrote (`written by ytm` in `.nfo` and cover files, `[by:ytm · ...]` in `.lrc` files) are still recognised as mfetchy's own. The command is now `mfy`.

Environment variables are `MFY_*` (for example `MFY_DEBUG`, `MFY_RESOLVER`, `MFY_SEARCH`, `MFY_SOUNDCLOUD`, `MFY_VERSION` and `MFY_INSTALL_DIR` for the installers). The old `YTM_*` names still work; the `MFY_` one wins when both are set.

## Development

`just` lists the tasks: `just build`, `just install`, `just check`, `just get <url>`.
