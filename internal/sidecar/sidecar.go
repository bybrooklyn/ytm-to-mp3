// Package sidecar writes the files media servers look for next to the music: folder.jpg
// (and optionally cover.png) with the album's cover, album.nfo in album folders and
// artist.nfo in artist folders. Everything mfetchy writes carries a marker ("written by mfetchy");
// a file without it was made by the user and is never touched. A second run changes
// nothing: files are rewritten only when their content would differ (nfo) or a larger
// cover is now available (images).
package sidecar

import (
	"bytes"
	"context"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/bybrooklyn/mfetchy/internal/names"
	"github.com/bybrooklyn/mfetchy/internal/store"
	"github.com/bybrooklyn/mfetchy/internal/tag"
)

// Marker is the text that says mfetchy wrote a file.
const Marker = "written by mfetchy"

// OldMarker is what files written before the rename carry; they still count as ours.
const OldMarker = "written by ytm"

// CoverPx is the largest side of the album art files.
const CoverPx = 1200

var audioExt = map[string]bool{".mp3": true, ".m4a": true, ".opus": true, ".flac": true, ".ogg": true}

// Options says what to write and how to read the music. The hooks default to ffprobe and
// ffmpeg; tests replace them.
type Options struct {
	FFmpeg, FFprobe string
	JPG, NFO, PNG   bool // folder.jpg, album.nfo + artist.nfo, cover.png

	Probe   func(ctx context.Context, path string) (tag.Info, error)
	Extract func(ctx context.Context, audio, dst string) error // the embedded picture
	Square  func(ctx context.Context, src, dst string) error   // square JPEG of at most CoverPx
}

// Result counts what was (or, in a preview, would be) done.
type Result struct {
	Albums, Artists int // folders looked at
	JPG, PNG        int // images written
	AlbumNFO        int
	ArtistNFO       int
	Kept            int // files without the marker, left alone
}

// Changed is the number of files written.
func (r Result) Changed() int { return r.JPG + r.PNG + r.AlbumNFO + r.ArtistNFO }

func (o *Options) defaults() {
	if o.Probe == nil {
		o.Probe = func(ctx context.Context, p string) (tag.Info, error) { return tag.Probe(ctx, o.FFprobe, p) }
	}
	if o.Extract == nil {
		o.Extract = func(ctx context.Context, audio, dst string) error { return tag.ExtractCover(ctx, o.FFmpeg, audio, dst) }
	}
	if o.Square == nil {
		o.Square = func(ctx context.Context, src, dst string) error {
			a := names.SquareCoverArgs(src, dst, CoverPx)
			if a == nil {
				return errors.New("not a usable image")
			}
			return exec.CommandContext(ctx, o.FFmpeg, a...).Run()
		}
	}
}

// Sync writes the sidecars of the album folders holding the given songs (and the artist
// folders above them), looking at every song in those folders. Songs outside root/Artist/
// Album are ignored. With apply false nothing is written and the Result is a preview.
func Sync(ctx context.Context, o Options, root string, songs []string, apply bool) (Result, error) {
	o.defaults()
	var res Result
	if !o.JPG && !o.NFO && !o.PNG {
		return res, nil
	}
	root = filepath.Clean(root)
	albums := map[string]bool{}
	artists := map[string]bool{}
	for _, s := range songs {
		if !audioExt[strings.ToLower(filepath.Ext(s))] {
			continue
		}
		al := filepath.Dir(filepath.Clean(s))
		ar := filepath.Dir(al)
		rel, err := filepath.Rel(root, al)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if err != nil || len(parts) != 2 || strings.HasPrefix(parts[0], ".") || strings.HasPrefix(parts[1], ".") || parts[0] == ".." {
			continue // not Artist/Album/song
		}
		albums[al] = true
		artists[ar] = true
	}
	for _, al := range sortedKeys(albums) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := syncAlbum(ctx, &o, al, apply, &res); err != nil {
			return res, err
		}
	}
	if o.NFO {
		for _, ar := range sortedKeys(artists) {
			if err := ctx.Err(); err != nil {
				return res, err
			}
			if err := syncArtist(ctx, &o, ar, apply, &res); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// songsIn lists the audio files directly in dir, sorted.
func songsIn(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") && audioExt[strings.ToLower(filepath.Ext(e.Name()))] {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// songsUnder lists the audio files below dir (not hidden folders), sorted.
func songsUnder(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && !strings.HasPrefix(d.Name(), ".") && audioExt[strings.ToLower(filepath.Ext(p))] {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// probeAll reads the tags of files, a few at a time; failures are left out.
func probeAll(ctx context.Context, o *Options, files []string) []tag.Info {
	infos := make([]tag.Info, len(files))
	ok := make([]bool, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if in, err := o.Probe(ctx, f); err == nil {
				in.Path = f
				infos[i], ok[i] = in, true
			}
		}()
	}
	wg.Wait()
	var out []tag.Info
	for i := range infos {
		if ok[i] {
			out = append(out, infos[i])
		}
	}
	return out
}

func syncAlbum(ctx context.Context, o *Options, dir string, apply bool, res *Result) error {
	files := songsIn(dir)
	if len(files) == 0 {
		return nil
	}
	infos := probeAll(ctx, o, files)
	if len(infos) == 0 {
		return nil
	}
	res.Albums++
	if o.JPG || o.PNG {
		if err := syncCovers(ctx, o, dir, infos, apply, res); err != nil {
			return err
		}
	}
	if o.NFO {
		doc := albumNFO(dir, infos)
		writeText(filepath.Join(dir, "album.nfo"), doc, apply, &res.AlbumNFO, &res.Kept)
	}
	return nil
}

func syncArtist(ctx context.Context, o *Options, dir string, apply bool, res *Result) error {
	files := songsUnder(dir)
	if len(files) == 0 {
		return nil
	}
	infos := probeAll(ctx, o, files)
	if len(infos) == 0 {
		return nil
	}
	res.Artists++
	writeText(filepath.Join(dir, "artist.nfo"), artistNFO(dir, infos), apply, &res.ArtistNFO, &res.Kept)
	return nil
}

// owned reports whether the file at p carries the marker in its first bytes.
func owned(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	head := buf[:n]
	return bytes.Contains(head, []byte(Marker)) || bytes.Contains(head, []byte(OldMarker))
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// writeText writes an nfo document unless a file of the user's is there or nothing changed.
func writeText(p string, doc []byte, apply bool, counter, kept *int) {
	if exists(p) {
		if !owned(p) {
			*kept++
			return
		}
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, doc) {
			return
		}
	}
	*counter++
	if apply {
		_ = writeAtomic(p, doc)
	}
}

func writeAtomic(p string, data []byte) error {
	tf, err := os.CreateTemp(filepath.Dir(p), ".mfy-*")
	if err != nil {
		return err
	}
	tmp := tf.Name()
	_, werr := tf.Write(data)
	cerr := tf.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		return errors.Join(werr, cerr)
	}
	_ = os.Chmod(tmp, 0o644)
	if err := store.Replace(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// bestCover picks the song whose embedded picture is the best album art: the largest
// square one, else the largest of any shape. ok is false when no song has a picture.
func bestCover(infos []tag.Info) (best tag.Info, side int, ok bool) {
	bestSquare := false
	for _, in := range infos {
		if !in.HasCover {
			continue
		}
		w, h := in.CoverW, in.CoverH
		s := min(w, h)
		sq := w > 0 && (w-h)*(w-h) <= 4
		switch {
		case !ok, sq && !bestSquare, sq == bestSquare && s > side:
			best, side, bestSquare, ok = in, s, sq, true
		}
	}
	if ok && side == 0 {
		side = 1 // size unknown: any file of ours is enough
	}
	return best, min(side, CoverPx), ok
}

func syncCovers(ctx context.Context, o *Options, dir string, infos []tag.Info, apply bool, res *Result) error {
	best, side, ok := bestCover(infos)
	if !ok {
		return nil
	}
	jpgPath, pngPath := filepath.Join(dir, "folder.jpg"), filepath.Join(dir, "cover.png")
	needJPG := o.JPG && wantImage(jpgPath, side, res)
	needPNG := o.PNG && wantImage(pngPath, side, res)
	if !needJPG && !needPNG {
		return nil
	}
	if !apply {
		if needJPG {
			res.JPG++
		}
		if needPNG {
			res.PNG++
		}
		return nil
	}
	work, err := os.MkdirTemp(dir, ".mfy-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(work)
	raw, sq := filepath.Join(work, "raw"), filepath.Join(work, "sq.jpg")
	if o.Extract(ctx, best.Path, raw) != nil || o.Square(ctx, raw, sq) != nil {
		return ctx.Err()
	}
	data, err := os.ReadFile(sq)
	if err != nil {
		return nil
	}
	if needJPG {
		if out, err := markJPEG(data); err == nil && writeAtomic(jpgPath, out) == nil {
			res.JPG++
		}
	}
	if needPNG {
		if out, err := jpegToPNG(data); err == nil && writeAtomic(pngPath, out) == nil {
			res.PNG++
		}
	}
	return nil
}

// wantImage says whether the image at p should be (re)written for a cover of the given
// side: it is missing, or mfetchy's own and smaller. A user's file counts as kept.
func wantImage(p string, side int, res *Result) bool {
	if !exists(p) {
		return true
	}
	if !owned(p) {
		res.Kept++
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return true // ours but broken
	}
	return min(cfg.Width, cfg.Height) < side
}

// markJPEG puts the marker in a JPEG comment segment right after the start-of-image
// marker; the picture itself is untouched.
func markJPEG(data []byte) ([]byte, error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, errors.New("not a jpeg")
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil {
		return nil, err
	}
	com := []byte(Marker)
	out := make([]byte, 0, len(data)+len(com)+4)
	out = append(out, 0xFF, 0xD8, 0xFF, 0xFE, byte((len(com)+2)>>8), byte(len(com)+2))
	out = append(out, com...)
	return append(out, data[2:]...), nil
}

// jpegToPNG re-encodes a JPEG as PNG (Go's standard encoder) with the marker in a tEXt
// chunk right after IHDR.
func jpegToPNG(data []byte) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&b, img); err != nil {
		return nil, err
	}
	raw := b.Bytes()
	const ihdrEnd = 8 + 4 + 4 + 13 + 4
	if len(raw) < ihdrEnd {
		return nil, errors.New("short png")
	}
	body := append([]byte("tEXt"), []byte("Comment\x00"+Marker)...)
	chunk := make([]byte, 0, len(body)+8)
	n := len(body) - 4
	chunk = append(chunk, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	chunk = append(chunk, body...)
	crc := crc32.ChecksumIEEE(body)
	chunk = append(chunk, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	out := append([]byte{}, raw[:ihdrEnd]...)
	out = append(out, chunk...)
	return append(out, raw[ihdrEnd:]...), nil
}
