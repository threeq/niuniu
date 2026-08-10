package imageopt

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strings"
)

type detected struct {
	Format    string // "png" | "jpeg" | "webp" | "gif" | "svg" | "unknown"
	Animated  bool   // multi-frame GIF or animated WebP
	SizeBytes int64
}

var (
	magicPNG  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	magicJPEG = []byte{0xff, 0xd8, 0xff}
	magicGIF  = []byte("GIF8")
	magicRIFF = []byte("RIFF")
	magicWEBP = []byte("WEBP")
)

// detect classifies a file by magic number + lightweight format probing.
// Never returns error for "not an image" — that's communicated via
// Format="unknown". Returns error only for true IO failures.
func detect(path string) (detected, error) {
	f, err := os.Open(path)
	if err != nil {
		return detected{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return detected{}, err
	}

	head := make([]byte, 64)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return detected{}, err
	}
	head = head[:n]

	d := detected{SizeBytes: info.Size(), Format: "unknown"}

	switch {
	case bytes.HasPrefix(head, magicPNG):
		d.Format = "png"
	case bytes.HasPrefix(head, magicJPEG):
		d.Format = "jpeg"
	case bytes.HasPrefix(head, magicGIF):
		d.Format = "gif"
		d.Animated = gifIsAnimated(path)
	case bytes.HasPrefix(head, magicRIFF) && len(head) >= 12 && bytes.Equal(head[8:12], magicWEBP):
		d.Format = "webp"
		d.Animated = webpIsAnimated(head)
	default:
		// SVG: XML-like with <svg in head, or shebang <?xml then <svg
		s := strings.ToLower(string(head))
		if strings.Contains(s, "<svg") || strings.HasPrefix(strings.TrimSpace(s), "<?xml") {
			d.Format = "svg"
		}
	}

	return d, nil
}

// gifIsAnimated scans the GIF byte stream for the second image
// descriptor (0x2C) without fully decoding any frame. Returns true as
// soon as the second descriptor is seen. Worst-case O(filesize) but
// only reads/scans bytes — no pixel decode, no allocation per frame.
// 修复 review I3：旧实现 gif.DecodeAll 对 50MB 动图会真解码所有帧。
func gifIsAnimated(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	return countGIFImageDescriptors(f, 2) >= 2
}

// countGIFImageDescriptors scans GIF blocks and counts up to `limit`
// image descriptor markers (0x2C). Returns the count up to limit; scan
// stops as soon as count reaches limit. Block layout per GIF89a spec:
//
//	header(6) + logical screen descriptor(7) + optional global color
//	table + sequence of blocks (extension 0x21 / image 0x2C / trailer 0x3B).
//
// Each block has its own sub-block chain that we skip without parsing.
func countGIFImageDescriptors(r io.Reader, limit int) int {
	br := bufio.NewReader(r)

	// header 6 bytes (GIF87a / GIF89a)
	if _, err := br.Discard(6); err != nil {
		return 0
	}
	// logical screen descriptor 7 bytes
	lsd := make([]byte, 7)
	if _, err := io.ReadFull(br, lsd); err != nil {
		return 0
	}
	// global color table?
	if lsd[4]&0x80 != 0 {
		size := 3 * (1 << (uint(lsd[4]&0x07) + 1))
		if _, err := br.Discard(size); err != nil {
			return 0
		}
	}

	count := 0
	for count < limit {
		b, err := br.ReadByte()
		if err != nil {
			return count
		}
		switch b {
		case 0x3B: // trailer
			return count
		case 0x21: // extension
			if _, err := br.Discard(1); err != nil { // label
				return count
			}
			if err := skipGIFSubBlocks(br); err != nil {
				return count
			}
		case 0x2C: // image descriptor
			count++
			// 9-byte image descriptor body
			body := make([]byte, 9)
			if _, err := io.ReadFull(br, body); err != nil {
				return count
			}
			// local color table?
			if body[8]&0x80 != 0 {
				size := 3 * (1 << (uint(body[8]&0x07) + 1))
				if _, err := br.Discard(size); err != nil {
					return count
				}
			}
			// LZW minimum code size (1 byte) + sub-block chain
			if _, err := br.Discard(1); err != nil {
				return count
			}
			if err := skipGIFSubBlocks(br); err != nil {
				return count
			}
		default:
			// unknown block — bail
			return count
		}
	}
	return count
}

func skipGIFSubBlocks(br *bufio.Reader) error {
	for {
		size, err := br.ReadByte()
		if err != nil {
			return err
		}
		if size == 0 {
			return nil
		}
		if _, err := br.Discard(int(size)); err != nil {
			return err
		}
	}
}

// webpIsAnimated reads the WebP container header. ANIM bit in the
// VP8X flags byte signals animation. Returns false for VP8 (static
// lossy) and VP8L (static lossless) containers.
func webpIsAnimated(head []byte) bool {
	if len(head) < 30 {
		return false
	}
	// RIFF{4} size{4} WEBP{4} VP8X{4} ...flags...
	if !bytes.Equal(head[12:16], []byte("VP8X")) {
		return false // VP8 or VP8L = static
	}
	// VP8X chunk size is at head[16:20] (4 bytes LE), then 4 bytes of
	// flags. Animation flag is bit 1 (0x02) of the first flag byte.
	if len(head) < 24 {
		return false
	}
	flags := head[20]
	return flags&0x02 != 0
}
