package basispoints

// Versioned, bounded transforms of existing inline PNG/JPEG images. No network
// downloads; animated or orientation/profile-bearing images stay unchanged.
import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"strings"

	"golang.org/x/image/draw"
)

const messageImageMaxPixels = 16 * 1024 * 1024

// Mode 0 re-encodes PNG without changing pixels; mode 1 bounds the long edge
// at 2048 and encodes opaque pixels as JPEG quality 85, transparency as PNG.
// Keep this implementation stable: persisted checkpoints can request version 1.
type MessageImageVariant struct {
	Version int
	Mode    int
}

type messageImageResult struct {
	URL    string
	Bytes  int64
	Reason string
}

type messageImageReader struct {
	ctx context.Context
	r   io.Reader
}

func (r messageImageReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type messageImageWriter struct {
	ctx context.Context
	bytes.Buffer
	limit int
}

func (w *messageImageWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > w.limit-w.Len() {
		return 0, fmt.Errorf("message image encoding budget exceeded")
	}
	return w.Buffer.Write(p)
}

func transformMessageImage(ctx context.Context, url string, variant MessageImageVariant) (messageImageResult, error) {
	skip := func(reason string) (messageImageResult, error) { return messageImageResult{Reason: reason}, nil }
	if variant.Version != 1 || (variant.Mode != 0 && variant.Mode != 1) {
		return messageImageResult{}, fmt.Errorf("unsupported message image transform version")
	}
	mime, payload, err := relayImagePayload(url, 20)
	if err != nil {
		return skip("invalid_image")
	}
	if mime != "image/png" && mime != "image/jpeg" {
		return skip("unsupported_format")
	}
	newReader := func() io.Reader {
		return messageImageReader{ctx, base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload))}
	}
	config, format, err := image.DecodeConfig(newReader())
	if err != nil || "image/"+format != mime {
		return skip("invalid_image")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > messageImageMaxPixels/config.Height {
		return skip("pixel_limit")
	}
	data, err := io.ReadAll(io.LimitReader(newReader(), (20<<20)+1))
	if ctx.Err() != nil {
		return messageImageResult{}, ctx.Err()
	}
	if err != nil || len(data) == 0 || len(data) > 20<<20 {
		return skip("invalid_image")
	}
	if !messageImageMetadataSafe(data, format) {
		return skip("animation_or_metadata")
	}
	if variant.Mode == 0 && format != "png" {
		return skip("no_lossless_encoder")
	}
	pixels, decodedFormat, err := image.Decode(messageImageReader{ctx, bytes.NewReader(data)})
	if ctx.Err() != nil {
		return messageImageResult{}, ctx.Err()
	}
	if err != nil || decodedFormat != format || pixels.Bounds().Dx() != config.Width || pixels.Bounds().Dy() != config.Height {
		return skip("invalid_image")
	}
	if variant.Mode == 1 {
		// Avoid silently reducing 16-bit PNG precision through an 8-bit buffer.
		switch pixels.(type) {
		case *image.NRGBA64, *image.RGBA64, *image.Gray16:
			return skip("high_bit_depth")
		}
		w, h := config.Width, config.Height
		if max(w, h) > 2048 {
			if w >= h {
				h, w = max(1, h*2048/w), 2048
			} else {
				w, h = max(1, w*2048/h), 2048
			}
			resized := image.NewNRGBA(image.Rect(0, 0, w, h))
			// The bounded scaler is synchronous; cancellation is checked on both
			// sides, not implemented by abandoning a still-running worker.
			if err := ctx.Err(); err != nil {
				return messageImageResult{}, err
			}
			draw.CatmullRom.Scale(resized, resized.Bounds(), pixels, pixels.Bounds(), draw.Src, nil)
			if err := ctx.Err(); err != nil {
				return messageImageResult{}, err
			}
			pixels = resized
		}
	}
	writer := &messageImageWriter{ctx: ctx, limit: len(data) - 1}
	outputMIME := "image/png"
	opaque, ok := pixels.(interface{ Opaque() bool })
	if variant.Mode == 1 && ok && opaque.Opaque() {
		outputMIME = "image/jpeg"
		err = jpeg.Encode(writer, pixels, &jpeg.Options{Quality: 85})
	} else {
		encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
		err = encoder.Encode(writer, pixels)
	}
	if ctx.Err() != nil {
		return messageImageResult{}, ctx.Err()
	}
	if err != nil || writer.Len() >= len(data) {
		return skip("not_smaller")
	}
	return messageImageResult{
		URL:   "data:" + outputMIME + ";base64," + base64.StdEncoding.EncodeToString(writer.Bytes()),
		Bytes: int64(writer.Len()),
	}, nil
}

func messageImageMetadataSafe(data []byte, format string) bool {
	if format == "png" {
		for offset := 8; offset+12 <= len(data); {
			n := int64(binary.BigEndian.Uint32(data[offset:]))
			if n > int64(len(data)-offset-12) {
				return false
			}
			switch string(data[offset+4 : offset+8]) {
			case "acTL", "iCCP", "eXIf", "gAMA", "cHRM":
				return false
			case "IEND":
				return true
			}
			offset += int(n) + 12
		}
		return false
	}
	// Reject EXIF, ICC and other application metadata instead of rotating or
	// recolouring pixels accidentally. Plain JFIF (APP0) is safe to re-encode.
	for offset := 2; offset+1 < len(data); {
		if data[offset] != 0xff {
			return false
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			return false
		}
		marker := data[offset]
		offset++
		if marker == 0xda {
			return true
		}
		if marker >= 0xe1 && marker <= 0xef {
			return false
		}
		if offset+2 > len(data) {
			return false
		}
		n := int(binary.BigEndian.Uint16(data[offset:]))
		if n < 2 || n > len(data)-offset {
			return false
		}
		offset += n
	}
	return false
}
