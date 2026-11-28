package httputil

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
)

type inboundSizeKey struct{}

type measuredBody struct {
	io.ReadCloser
	expected, read int64
	complete       *atomic.Int64
}

func (b *measuredBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read += int64(n)
	if err == io.EOF || (err == nil && b.expected > 0 && b.read == b.expected) {
		b.complete.Store(b.read)
	}
	return n, err
}

// MeasureInboundBody counts the original HTTP body before decoding or model rewriting.
func MeasureInboundBody(req *http.Request) *http.Request {
	if req.Body == nil || req.Method == http.MethodGet || req.Method == http.MethodHead {
		return req
	}
	size := new(atomic.Int64)
	size.Store(-1)
	req = req.WithContext(context.WithValue(req.Context(), inboundSizeKey{}, size))
	if req.Body == http.NoBody {
		size.Store(0)
	} else {
		req.Body = &measuredBody{ReadCloser: req.Body, expected: req.ContentLength, complete: size}
	}
	return req
}

func InboundBodySize(ctx context.Context) *int64 {
	if size, ok := ctx.Value(inboundSizeKey{}).(*atomic.Int64); ok {
		if n := size.Load(); n >= 0 {
			return &n
		}
	}
	return nil
}

// CarryInboundBodySize passes only the counter to an asynchronous usage task.
func CarryInboundBodySize(dst, src context.Context) context.Context {
	if size, ok := src.Value(inboundSizeKey{}).(*atomic.Int64); ok {
		return context.WithValue(dst, inboundSizeKey{}, size)
	}
	return dst
}
