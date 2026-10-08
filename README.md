# ytm

Download your YouTube Music library as properly tagged audio files. One small binary for Windows, macOS and Linux.

```
ytm                  # guided: finds or helps you get your export, then downloads it
ytm takeout.zip      # or point it straight at your Google Takeout export
```

That's it. On first run ytm fetches the tools it needs (yt-dlp, ffmpeg and a JavaScript runtime), asks once whether it may use your browser's YouTube login for faster, less throttled downloads, and then downloads everything with embedded cover art.

## Install

| Platform | Command |
|---|---|
| Linux / macOS | `curl -fsSL https://raw.githubusercontent.com/bybrooklyn/ytm-to-mp3/main/install.sh \| sh` |
| Windows (PowerShell) | `irm https://raw.githubusercontent.com/bybrooklyn/ytm-to-mp3/main/install.ps1 \| iex` |
| Anything | download a binary from the [releases page](https://github.com/bybrooklyn/ytm-to-mp3/releases) (`.deb`/`.rpm` too) |
| From source | `go install github.com/bybrooklyn/ytm-to-mp3/cmd/ytm@latest` |

## Getting your export

Run `ytm export`. It opens [Google Takeout](https://takeout.google.com/), tells you exactly which boxes to tick (YouTube and YouTube Music → *music (library and uploads)*), waits for the zip to land in your Downloads folder and starts downloading by itself. Google can't be automated, so the clicking is yours.

## Commands

```
ytm [export]                 download a Takeout .zip / folder / .csv, a text file of links, or a playlist URL
ytm export                   guide me through getting the export
ytm tag <folder>             improve metadata (preview; add --apply to write, --undo to revert)
ytm update                   refresh yt-dlp
```

Options: `-o DIR` (default `./Music`), `-f mp3|m4a|opus|flac`, `-j N` max parallel downloads, `--no-cookies`, `--dry-run`, `--retry`, `--all`, `-y`.

Re-running is always safe: finished tracks are skipped, temporary failures retry, and tracks YouTube says are unavailable are remembered (`--retry` tries them again). Files already named `... [id].ext` from an older run are recognised too.

## How it stays fast without getting blocked

- A three-stage pipeline (resolve → fetch → encode) with separate worker pools, so slow YouTube lookups, network transfers and CPU-bound encoding all overlap.
- Audio is pulled in parallel 10 MB ranges per file, which avoids YouTube's per-connection throttling. If a stream can't be fetched directly, yt-dlp downloads it instead.
- Concurrency is adaptive: it ramps up while requests succeed, halves and pauses with jittered backoff the moment YouTube pushes back (HTTP 429, "confirm you're not a bot"), and rotates YouTube player clients on retries. `-j` only sets the ceiling.
- m4a/opus output is remuxed without re-encoding when the source already matches.

## Cookies

If you agree, ytm reads your YouTube login from an installed browser (Firefox, Chrome, Edge, Brave, Chromium, Vivaldi, Opera, Safari) through yt-dlp, keeps a private copy in your cache directory and sends it only to YouTube. Logged-in requests get much higher limits. Say no and it runs anonymously. Note that newer Chrome/Edge on Windows lock their cookie storage; ytm falls back to another browser and Firefox is the reliable choice there. Use `--no-cookies` to skip entirely.

## Metadata

Everything YouTube knows is kept: title, artist(s), album, release or upload date, genre, track number, a link back to the video, and square cover art. The CSV from Google takes priority over what YouTube reports.

`ytm tag` improves files you already have, and it is deliberately cautious:

- Removes obvious upload noise (`(Official Video)`, `[HD]`, `- Topic`, `Artist - ` title prefixes). Never touches `(Live)`, `(Remastered)` and similar.
- Asks [MusicBrainz](https://musicbrainz.org) about each song and **only acts when it is essentially certain**: same title, same artist, length within 2 seconds, top search score and no rival recording. Then it adds the MusicBrainz IDs and ISRC, fixes spelling, and fills album, date, track number and cover art only when the album is unambiguous.
- Anything less certain is left exactly as YouTube tagged it.
- It previews by default, `--apply` writes, `--undo` restores the previous tags, cover art and file names. `--rename` also moves files into `Artist/Album/Title [id].ext`; `--offline` skips MusicBrainz.

## Good to know

- Songs you uploaded yourself (not public on YouTube) usually can't be downloaded.
- Windows and macOS builds are compiled and unit-tested in CI; most real-world testing so far has been on Linux. Bug reports welcome.
- Only download music you have the right to. This tool automates what yt-dlp does; respect YouTube's terms and copyright law where you live.

MIT licensed.
