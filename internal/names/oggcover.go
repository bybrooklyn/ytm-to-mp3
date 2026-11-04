package names

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeConfig for the picture block's size
	"os"
	"sort"
	"strings"
)

// Ogg Opus has no picture stream: cover art is a Vorbis comment holding a base64 FLAC
// picture block (METADATA_BLOCK_PICTURE), which ffmpeg reads back as cover art but can't
// write from a picture input. ytm builds the comment itself and hands it to ffmpeg in an
// ffmetadata file (it is far too long for a command-line argument).

// maxCoverBytes bounds the image put into a comment (players choke on huge headers).
const maxCoverBytes = 4 << 20

// SquareCoverArgs is the ffmpeg command line that turns an untrusted cover image (jpeg,
// png or webp, decoder forced) into a square JPEG at most maxPx wide. nil if src isn't one
// of those formats.
func SquareCoverArgs(src, dst string, maxPx int) []string {
	dec := ImageDecoder(src)
	if dec == "" {
		return nil
	}
	return []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-protocol_whitelist", "file,pipe", "-f", "image2", "-pattern_type", "none", "-c:v", dec, "-i", FFPath(src),
		"-vf", fmt.Sprintf("crop='min(iw,ih)':'min(iw,ih)',scale='min(%d,iw)':-1", maxPx),
		"-frames:v", "1", "-c:v", "mjpeg", "-q:v", "2", "-f", "image2", "-update", "1", FFPath(dst)}
}

// WriteOggMeta writes an ffmetadata file with tags and, if jpegPath is set, that JPEG as
// the cover. Use it as input 1 with OggMetaArgs.
func WriteOggMeta(dst string, tags map[string]string, jpegPath string) error {
	var b strings.Builder
	b.WriteString(";FFMETADATA1\n")
	keys := make([]string, 0, len(tags))
	for k, v := range tags {
		if v != "" && k != "metadata_block_picture" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(escapeMeta(k) + "=" + escapeMeta(StripControl(tags[k])) + "\n")
	}
	if jpegPath != "" {
		block, err := pictureBlock(jpegPath)
		if err != nil {
			return err
		}
		b.WriteString("METADATA_BLOCK_PICTURE=" + escapeMeta(base64.StdEncoding.EncodeToString(block)) + "\n")
	}
	return os.WriteFile(dst, []byte(b.String()), 0o600)
}

// OggMetaArgs are the ffmpeg options (after input 0, the audio) that take every tag from
// the ffmetadata file meta and none from the source.
func OggMetaArgs(meta string) []string {
	return []string{"-f", "ffmetadata", "-protocol_whitelist", "file,pipe", "-i", FFPath(meta),
		"-map", "0:a:0", "-map_metadata", "-1", "-map_metadata:s:a", "1"}
}

// pictureBlock is a FLAC METADATA_BLOCK_PICTURE for a front cover JPEG.
func pictureBlock(jpegPath string) ([]byte, error) {
	img, err := os.ReadFile(jpegPath)
	if err != nil {
		return nil, err
	}
	if len(img) > maxCoverBytes || imageDecoderOf(img) != "mjpeg" {
		return nil, errors.New("cover is not a usable JPEG")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(img))
	if err != nil {
		return nil, err
	}
	const mime = "image/jpeg"
	var b bytes.Buffer
	be := func(v uint32) { _ = binary.Write(&b, binary.BigEndian, v) }
	be(3) // front cover
	be(uint32(len(mime)))
	b.WriteString(mime)
	be(0) // no description
	be(uint32(cfg.Width))
	be(uint32(cfg.Height))
	be(24) // colour depth
	be(0)  // not indexed
	be(uint32(len(img)))
	b.Write(img)
	return b.Bytes(), nil
}

// escapeMeta escapes the characters ffmetadata gives meaning to.
func escapeMeta(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '=', ';', '#', '\\', '\n':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
