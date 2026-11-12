package basispoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type messageImageEntry struct {
	result        messageImageResult
	expires, used time.Time
}

// MessageImageCache stores only derived encoded images, in process memory.
// Its zero value is ready to use. One decoder bounds pixel working memory;
// cache waiters do not start duplicate decodes and can cancel independently.
type MessageImageCache struct {
	mu      sync.Mutex
	entries map[[32]byte]messageImageEntry
	bytes   int
	gate    chan struct{}
}

func messageImageDigest(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

func (c *MessageImageCache) lookup(key [32]byte) (messageImageResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, entry := range c.entries {
		if !now.Before(entry.expires) {
			c.bytes -= len(entry.result.URL)
			delete(c.entries, k)
		}
	}
	entry, ok := c.entries[key]
	if ok {
		entry.used = now
		c.entries[key] = entry
	}
	return entry.result, ok
}

func (c *MessageImageCache) transform(ctx context.Context, scope, url string, v MessageImageVariant) (messageImageResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return messageImageResult{}, false, err
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d/%d\x00%s", scope, v.Version, v.Mode, messageImageDigest(url))))
	if result, ok := c.lookup(key); ok {
		return result, true, nil
	}
	c.mu.Lock()
	if c.gate == nil {
		c.gate = make(chan struct{}, 1)
	}
	gate := c.gate
	c.mu.Unlock()
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return messageImageResult{}, false, ctx.Err()
	}
	defer func() { <-gate }()
	if err := ctx.Err(); err != nil {
		return messageImageResult{}, false, err
	}
	if result, ok := c.lookup(key); ok {
		return result, true, nil
	}
	result, err := transformMessageImage(ctx, url, v)
	if ctx.Err() != nil {
		return messageImageResult{}, false, ctx.Err()
	}
	if err != nil {
		return result, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[[32]byte]messageImageEntry)
	}
	const maxBytes = 64 << 20
	if len(result.URL) > maxBytes {
		return result, false, nil
	}
	for len(c.entries) >= 128 || c.bytes+len(result.URL) > maxBytes {
		var oldest [32]byte
		var used time.Time
		for k, entry := range c.entries {
			if used.IsZero() || entry.used.Before(used) {
				oldest, used = k, entry.used
			}
		}
		c.bytes -= len(c.entries[oldest].result.URL)
		delete(c.entries, oldest)
	}
	now := time.Now()
	c.entries[key] = messageImageEntry{result: result, expires: now.Add(30 * time.Minute), used: now}
	c.bytes += len(result.URL)
	return result, false, nil
}
