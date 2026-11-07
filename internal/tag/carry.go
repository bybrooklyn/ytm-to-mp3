package tag

import (
	"context"
	"os"
	"path/filepath"

	"github.com/bybrooklyn/mfetchy/internal/meta"
	"github.com/bybrooklyn/mfetchy/internal/names"
)

// carriedTags are the canonical tags a replacement keeps from the file it replaces (see
// meta.Merge for the rule per tag). Everything else in a file (encoder, container brands)
// is the new file's own.
var carriedTags = []string{
	"title", "artist", "album", "album_artist", "date", "genre", "comment", "composer", "copyright",
	"label", "barcode", "track", "disc", "isrc", "explicit", "lyrics",
	"musicbrainz_trackid", "musicbrainz_albumid", "musicbrainz_artistid", "musicbrainz_albumartistid",
	"musicbrainz_recordingid",
}

func pick(tags map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range carriedTags {
		if v := tags[k]; v != "" {
			out[k] = v
		}
	}
	return out
}

// CarryTags is called when newPath is about to take the place of oldPath (the same song
// from a better source): it merges both files' tags with meta.Merge and writes whatever
// the new file lacks or has worse into newPath, through ffmpeg like any tag write. The
// new file's own values win wherever its source is at least as trusted; an ISRC, MusicBrainz
// ids, genre, lyrics or release date only the old file knew are kept, and the larger
// square cover art is used. newSource and oldSource are meta provider names.
//
// It returns where each tag of the result came from (for the library's provenance); on
// any failure the new file is left as it was and the error is returned, so a caller can
// carry on with the replacement.
func CarryTags(ctx context.Context, ffmpeg, ffprobe, newPath, newSource, oldPath, oldSource string) (map[string]string, error) {
	newInfo, err := Probe(ctx, ffprobe, newPath)
	if err != nil {
		return nil, err
	}
	oldInfo, err := Probe(ctx, ffprobe, oldPath)
	if err != nil {
		return nil, err
	}
	merged := meta.Merge(
		meta.Set{Source: newSource, Tags: pick(newInfo.Tags), CoverW: newInfo.CoverW, CoverH: newInfo.CoverH},
		meta.Set{Source: oldSource, Tags: pick(oldInfo.Tags), CoverW: oldInfo.CoverW, CoverH: oldInfo.CoverH},
	)
	ext := filepath.Ext(newPath)
	set := map[string]string{}
	for k, v := range meta.Diff(pick(newInfo.Tags), merged.Tags) {
		// a total the file already says in its own fields (Vorbis) is not a change
		if names.TagSupported(ext, k) && !sameTrack(k, v, newInfo) {
			set[k] = v
		}
	}
	// "musicbrainz_recordingid" is an old name for the track id: don't carry it twice.
	delete(set, "musicbrainz_recordingid")
	cover := ""
	if merged.Cover == 1 && oldInfo.HasCover {
		tmp, err := os.CreateTemp(filepath.Dir(newPath), ".mfy-carry-*.jpg")
		if err == nil {
			tmp.Close()
			defer os.Remove(tmp.Name())
			if ExtractCover(ctx, ffmpeg, oldPath, tmp.Name()) == nil {
				cover = tmp.Name()
			}
		}
	}
	if len(set) == 0 && cover == "" {
		return merged.From, nil
	}
	if err := Write(ctx, ffmpeg, ffprobe, newPath, set, cover); err != nil {
		return nil, err
	}
	return merged.From, nil
}
