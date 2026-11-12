package basispoints

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func messageImageFixture(t *testing.T, w, h int, transparent bool) string {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if transparent {
				a = uint8(80 + x%160)
			}
			im.SetNRGBA(x, y, color.NRGBA{uint8(x*13 + y*7), uint8(x*3 + y*17), uint8((x/8 + y/8) % 2 * 255), a})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&buf, im))
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func messageImageURLItem(url string) object {
	return object{"role": "user", "content": []any{object{"type": "input_text", "text": "PRIVATE_TASK"}, object{"type": "input_image", "image_url": url, "detail": "original"}}}
}

func TestMessageImageCompressionPreservesCurrentTurnAndReplay(t *testing.T) {
	url := messageImageFixture(t, 512, 256, false)
	h := policyHistory(t, messageImageURLItem(url), message("assistant", "seen"), messageImageURLItem(url))
	before, _ := h.Body()
	cache := new(MessageImageCache)
	next, cp, obs, err := cache.CompressHistory(context.Background(), "account-A", h, 2, ImageBudget{20, 1 << 20, 650 << 10}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, obs.Changed)
	require.Zero(t, obs.Lossy)
	require.Less(t, obs.AfterBytes, obs.BeforeBytes)
	require.Equal(t, h.Input[2], next.Input[2], "current images and text must remain identical")
	after, _ := h.Body()
	require.Equal(t, before, after, "source request is immutable")
	require.Equal(t, h.Count, next.Count)
	part, _ := messageImagePart(next, 0, 1)
	require.Equal(t, "original", part["detail"])
	require.Equal(t, "PRIVATE_TASK", next.Input[0].(object)["content"].([]any)[0].(object)["text"])
	metadata, _ := json.Marshal(cp)
	require.NotContains(t, string(metadata), "data:")
	require.NotContains(t, string(metadata), "PRIVATE")
	_, _, warm, err := cache.CompressHistory(context.Background(), "account-A", h, 2, ImageBudget{20, 1 << 20, 650 << 10}, nil)
	require.NoError(t, err)
	require.Positive(t, warm.CacheHits)
	_, _, foreign, err := cache.CompressHistory(context.Background(), "account-B", h, 2, ImageBudget{20, 1 << 20, 650 << 10}, nil)
	require.NoError(t, err)
	require.Zero(t, foreign.CacheHits)
	// A cache miss after restart reproduces exactly the same receipt.
	replay := policyHistory(t, append(h.Input, message("user", "continue"))...)
	changed, err := new(MessageImageCache).Reconcile(context.Background(), "account-A", replay, []ImageCheckpoint{cp})
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, next.Input, replay.Input[:len(next.Input)])
	changed, err = cache.Reconcile(context.Background(), "account-A", replay, []ImageCheckpoint{cp})
	require.NoError(t, err)
	require.False(t, changed)
	// The complete current user turn stays protected even with later progress.
	turn := policyHistory(t, h.Input[0], message("assistant", "seen"), h.Input[2], object{"type": "function_call", "call_id": "x"}, object{"type": "function_call_output", "call_id": "x", "output": "done"})
	progress := ImageProgress{Count: 4, Digest: imageHistoryDigest(turn.Input[:4])}
	boundary, reason := MessageImageBoundary(turn, &progress)
	require.Equal(t, "ready", reason)
	require.Equal(t, 2, boundary)
}

func TestMessageImageCodecDownsamplesAndPreservesAlpha(t *testing.T) {
	for _, transparent := range []bool{false, true} {
		url := messageImageFixture(t, 2304, 128, transparent)
		result, err := transformMessageImage(context.Background(), url, MessageImageVariant{1, 1})
		require.NoError(t, err)
		require.NotEmpty(t, result.URL)
		// Persisted checkpoints require byte-stable v1 results across upgrades.
		want := "80d4a7d92d7d392ad65c7a0d3522d5f3e5fbce10d2cd653bbba764555208d551"
		if transparent {
			want = "d335d8726f8268edb26d17498b35d1c4547db0ea820d475fc6f8117616c76009"
		}
		require.Equal(t, want, messageImageDigest(result.URL))
		_, payload, _ := strings.Cut(result.URL, ",")
		im, format, err := image.Decode(base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload)))
		require.NoError(t, err)
		require.Equal(t, 2048, im.Bounds().Dx())
		if transparent {
			require.Equal(t, "png", format)
			_, _, _, a := im.At(100, 20).RGBA()
			require.Less(t, a, uint32(65535))
		} else {
			require.Equal(t, "jpeg", format)
		}
	}
	url := messageImageFixture(t, 512, 256, true)
	result, err := transformMessageImage(context.Background(), url, MessageImageVariant{1, 0})
	require.NoError(t, err)
	require.NotEmpty(t, result.URL)
	decode := func(url string) image.Image {
		_, payload, _ := strings.Cut(url, ",")
		im, _, e := image.Decode(base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload)))
		require.NoError(t, e)
		return im
	}
	before, after := decode(url), decode(result.URL)
	require.Equal(t, before.Bounds(), after.Bounds())
	for _, point := range []image.Point{{0, 0}, {100, 120}, {511, 255}} {
		require.Equal(t, before.At(point.X, point.Y), after.At(point.X, point.Y))
	}
}

func TestMessageImageCompressionFallsBackToLossy(t *testing.T) {
	// Fixed high-entropy pixels cannot fit this budget by PNG re-encoding alone.
	im := image.NewNRGBA(image.Rect(0, 0, 2304, 512))
	for y := 0; y < 512; y++ {
		for x := 0; x < 2304; x++ {
			v := uint32(x + 2304*y + 1)
			v ^= v >> 16
			v *= 0x7feb352d
			v ^= v >> 15
			v *= 0x846ca68b
			v ^= v >> 16
			im.SetNRGBA(x, y, color.NRGBA{uint8(v), uint8(v >> 8), uint8(v >> 16), 255})
		}
	}
	var encoded bytes.Buffer
	require.NoError(t, (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&encoded, im))
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	h := policyHistory(t, messageImageURLItem(url), message("assistant", "seen"), message("user", "continue"))
	next, cp, obs, err := new(MessageImageCache).CompressHistory(context.Background(), "lossy", h, 2, ImageBudget{20, 20 << 20, 1 << 20}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, obs.Lossy)
	require.Len(t, cp.MessageImages, 1)
	require.Less(t, MeasureImageInput(next.Input).Bytes, int64(1<<20))
	require.Equal(t, h.Input[2], next.Input[2])
}

func TestMessageImageValidationAndCancellation(t *testing.T) {
	url := messageImageFixture(t, 512, 256, false)
	_, payload, _ := strings.Cut(url, ",")
	data, err := base64.StdEncoding.DecodeString(payload)
	require.NoError(t, err)
	chunk := func(kind string, body []byte) []byte {
		out := make([]byte, 12+len(body))
		binary.BigEndian.PutUint32(out, uint32(len(body)))
		copy(out[4:], kind)
		copy(out[8:], body)
		binary.BigEndian.PutUint32(out[8+len(body):], crc32.ChecksumIEEE(out[4:8+len(body)]))
		return out
	}
	for _, kind := range []string{"acTL", "eXIf", "iCCP", "gAMA"} {
		modified := append(append(append([]byte{}, data[:33]...), chunk(kind, []byte{0, 0, 0, 1})...), data[33:]...)
		result, err := transformMessageImage(context.Background(), "data:image/png;base64,"+base64.StdEncoding.EncodeToString(modified), MessageImageVariant{1, 1})
		require.NoError(t, err)
		require.Empty(t, result.URL)
		require.Equal(t, "animation_or_metadata", result.Reason)
	}
	// A decompression bomb is rejected from dimensions before allocating pixels.
	large := append([]byte{}, data...)
	binary.BigEndian.PutUint32(large[16:20], 100000)
	binary.BigEndian.PutUint32(large[20:24], 100000)
	binary.BigEndian.PutUint32(large[29:33], crc32.ChecksumIEEE(large[12:29]))
	result, err := transformMessageImage(context.Background(), "data:image/png;base64,"+base64.StdEncoding.EncodeToString(large), MessageImageVariant{1, 1})
	require.NoError(t, err)
	require.Empty(t, result.URL)
	require.Equal(t, "pixel_limit", result.Reason)
	for _, bad := range []string{"data:image/png;base64,broken", strings.Replace(url, "image/png", "image/jpeg", 1), "https://example.invalid/private.png"} {
		result, err := transformMessageImage(context.Background(), bad, MessageImageVariant{1, 1})
		require.NoError(t, err)
		require.Empty(t, result.URL)
	}
	cache := new(MessageImageCache)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = cache.transform(ctx, "s", url, MessageImageVariant{1, 0})
	require.ErrorIs(t, err, context.Canceled)
	cache.gate = make(chan struct{}, 1)
	cache.gate <- struct{}{}
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := cache.transform(ctx, "s", url, MessageImageVariant{1, 0}); done <- err }()
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	<-cache.gate
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, e := cache.transform(context.Background(), "s", url, MessageImageVariant{1, 0})
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	cache.mu.Lock()
	require.Len(t, cache.entries, 1)
	require.LessOrEqual(t, cache.bytes, 64<<20)
	cache.mu.Unlock()
}

func TestMessageImageCheckpointsComposeAndCoalesce(t *testing.T) {
	ctx := context.Background()
	cache := new(MessageImageCache)
	url := messageImageFixture(t, 512, 256, false)
	h := policyHistory(t, messageImageURLItem(url), message("assistant", "seen"), messageImageURLItem(url))
	budget := ImageBudget{20, 1 << 20, 650 << 10}
	next, first, _, err := cache.CompressHistory(ctx, "s", h, 2, budget, nil)
	require.NoError(t, err)
	// As a later user turn arrives, the previously protected image can shrink.
	raw := policyHistory(t, append(append([]any{}, h.Input...), message("assistant", "seen again"), messageImageURLItem(url))...)
	replayed := copyMessageHistory(raw)
	_, err = cache.Reconcile(ctx, "s", replayed, []ImageCheckpoint{first})
	require.NoError(t, err)
	second, cp, obs, err := cache.CompressHistory(ctx, "s", replayed, 4, budget, []ImageCheckpoint{first})
	require.NoError(t, err)
	require.Equal(t, 1, obs.Changed)
	require.Equal(t, next.Input[0], second.Input[0], "never recompress a derived historical image")
	cps := cache.AppendCheckpoint(ctx, "s", raw, second, []ImageCheckpoint{first}, cp)
	require.Len(t, cps, 1)
	require.Len(t, cps[0].MessageImages, 2)
	check := copyMessageHistory(raw)
	_, err = cache.Reconcile(ctx, "s", check, cps)
	require.NoError(t, err)
	require.Equal(t, second.Input, check.Input)
	// Compaction checkpoints use the transformed prefix and replay in order.
	window := []any{object{"type": "compaction", "encrypted_content": "opaque"}}
	compact, err := second.Checkpoint(4, window)
	require.NoError(t, err)
	echo := policyHistory(t, append(append(append([]any{}, raw.Input...), window...), message("user", "continue"))...)
	_, err = cache.Reconcile(ctx, "s", echo, append(cps, compact))
	require.NoError(t, err)
	require.Equal(t, window[0], echo.Input[0])
	require.Equal(t, raw.Input[4], echo.Input[1])
	// A corrupted receipt cannot partially mutate the original input.
	broken := cps[0]
	broken.MessageImages = append([]MessageImagePosition{}, broken.MessageImages...)
	broken.MessageImages[1].Digest = "wrong"
	check = copyMessageHistory(raw)
	before, _ := check.Body()
	_, err = cache.Reconcile(ctx, "s", check, []ImageCheckpoint{broken})
	require.Error(t, err)
	after, _ := check.Body()
	require.Equal(t, before, after)
	// A prior tool trim must not leak mutations if a later compression receipt
	// fails: both operations are one atomic replay from the caller's perspective.
	tools := policyHistory(t, object{"type": "function_call", "call_id": "old", "name": "shot"}, object{"type": "function_call_output", "call_id": "old", "output": []any{object{"type": "input_image", "image_url": url}}}, h.Input[0], message("assistant", "seen"), message("user", "continue"))
	trimmed, trim, e := tools.TrimOldToolImages(4, ImageBudget{20, 1 << 20, 650 << 10})
	require.NoError(t, e)
	compressed, receipt, _, e := cache.CompressHistory(ctx, "s", trimmed, 4, ImageBudget{20, 1 << 20, 100 << 10}, nil)
	require.NoError(t, e)
	require.NotSame(t, trimmed, compressed)
	require.NotEmpty(t, receipt.MessageImages)
	receipt.MessageImages[0].Digest = "wrong"
	before, _ = tools.Body()
	_, e = cache.Reconcile(ctx, "s", tools, []ImageCheckpoint{trim, receipt})
	require.Error(t, e)
	after, _ = tools.Body()
	require.Equal(t, before, after)
}

func TestMessageImageCompressionRefusesProtectedAndUnsupportedHistory(t *testing.T) {
	url := messageImageFixture(t, 512, 256, false)
	cache := new(MessageImageCache)
	ctx := context.Background()
	h := policyHistory(t, messageImageURLItem(url), message("assistant", "seen"), messageImageURLItem(url))
	next, cp, obs, err := cache.CompressHistory(ctx, "s", h, 2, ImageBudget{20, 1 << 20, 100 << 10}, nil)
	require.NoError(t, err)
	require.Same(t, h, next)
	require.Empty(t, cp.MessageImages)
	require.Equal(t, "protected_turn_over_budget", obs.Reason)
	h = policyHistory(t, object{"type": "function_call_output", "call_id": "x", "output": []any{object{"type": "input_image", "image_url": url}}}, message("assistant", "seen"), message("user", "continue"))
	_, cp, obs, err = cache.CompressHistory(ctx, "s", h, 2, ImageBudget{20, 1 << 20, 100 << 10}, nil)
	require.NoError(t, err)
	require.Empty(t, cp.MessageImages)
	require.Equal(t, "no_candidates", obs.Reason)
	h = policyHistory(t, messageImageURLItem(url), message("assistant", "unfinished"))
	boundary, reason := MessageImageBoundary(h, nil)
	require.Zero(t, boundary)
	require.Equal(t, "boundary_unavailable", reason)
}
