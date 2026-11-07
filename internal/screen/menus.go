package screen

// Library item ids, as LibraryItems lists them.
const (
	LibAll        = "all"
	LibYouTube    = "youtube"
	LibBandcamp   = "bandcamp"
	LibTakeout    = "takeout"
	LibAppleMatch = "applematch"
)

// LibraryItems are the imports of the Library page. Each one reads a library, shows what it
// found and asks before downloading; the downloads themselves run on the Downloads page.
func LibraryItems() []MenuItem {
	return []MenuItem{
		{ID: LibAll, Group: "All at once", Label: "Download from all my accounts", Button: "Start...",
			Help: "Reads every account that is set up, merges them and downloads what is missing, from the best source each song has."},
		{ID: LibYouTube, Group: "One place at a time", Label: "YouTube Music library", Button: "Read library...",
			Help: "Reads your library from the browser that is signed in to music.youtube.com. No export needed."},
		{ID: LibBandcamp, Label: "Bandcamp purchases", Button: "Read purchases...",
			Help: "Downloads everything you bought on Bandcamp as the original files, FLAC when offered."},
		{ID: LibTakeout, Label: "Import a Takeout export", Button: "Choose export...",
			Help: "A Google Takeout .zip, an extracted folder or a library .csv. If you do not have one yet, a guide shows how to get it."},
		{ID: LibAppleMatch, Group: "Apple Music", Label: "Find my YouTube library on Apple Music", Button: "Find songs...",
			Help: "Looks up the songs of a YouTube Music export on Apple Music and downloads the matches as 256 kbps AAC. Needs an Apple Music sign-in."},
	}
}

// Tool item ids, as ToolItems lists them.
const (
	ToolDoctor   = "doctor"
	ToolVPNTest  = "vpntest"
	ToolUpdate   = "update"
	ToolTag      = "tag"
	ToolTidy     = "tidy"
	ToolLyrics   = "lyrics"
	ToolSidecars = "sidecars"
)

// ToolItems are the actions of the Tools page. Each one runs in the background with its
// output shown under the buttons.
func ToolItems() []MenuItem {
	return []MenuItem{
		{ID: ToolDoctor, Group: "Check", Label: "Check that everything works", Button: "Run check",
			Help: "Looks at the tools, the network, your sign-ins and the music folder, and says what to fix."},
		{ID: ToolVPNTest, Label: "Test VPN", Button: "Run test",
			Help: "Connects each server downloads would use and shows where it exits."},
		{ID: ToolUpdate, Label: "Update download tools", Button: "Update",
			Help: "Fetches the newest yt-dlp. ffmpeg and deno are downloaded when they are first needed."},
		{ID: ToolTag, Group: "Your music folder", Label: "Fix tags", Button: "Fix tags...",
			Help: "Corrects names, dates and track numbers from MusicBrainz. It shows what it would change first, and the last fix can be undone."},
		{ID: ToolTidy, Label: "Tidy file names and merge duplicate folders", Button: "Tidy...",
			Help: "Renames files to Artist/Album/01 - Title and merges folders that differ only in spelling. Nothing is deleted; Fix tags can undo it."},
		{ID: ToolLyrics, Label: "Fetch lyrics for my library", Button: "Fetch lyrics",
			Help: "Saves a .lrc file beside each song. LRCLIB gets the title, artist, album and length, nothing else."},
		{ID: ToolSidecars, Label: "Write cover and .nfo files for my library", Button: "Write files...",
			Help: "Shows what it would write, then writes folder.jpg and album and artist .nfo files. Files you made yourself are left alone."},
	}
}
