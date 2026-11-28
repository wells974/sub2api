package httputil

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInboundBodySizeSurvivesPartialReadAndTaskHandoff(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("123"))
	req.ContentLength = -1
	req = MeasureInboundBody(req)
	task := CarryInboundBodySize(context.Background(), req.Context())
	_, err := io.ReadFull(req.Body, make([]byte, 1))
	require.NoError(t, err)
	require.Nil(t, InboundBodySize(task))
	_, err = io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, int64(3), *InboundBodySize(task))

	empty := MeasureInboundBody(httptest.NewRequest(http.MethodPost, "/", http.NoBody))
	require.Equal(t, int64(0), *InboundBodySize(empty.Context()))
	ws := MeasureInboundBody(httptest.NewRequest(http.MethodGet, "/", nil))
	require.Nil(t, InboundBodySize(ws.Context()))
}

func TestInboundBodySizeRequiresACompleteSuccessfulRead(t *testing.T) {
	for _, tc := range []struct {
		name          string
		contentLength int64
		limit         int64
	}{
		{name: "early EOF", contentLength: 5, limit: 10},
		{name: "over declared length", contentLength: 2, limit: 10},
		{name: "over body limit", contentLength: 2, limit: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("abc"))
			req.ContentLength = tc.contentLength
			req.Body = http.MaxBytesReader(nil, req.Body, tc.limit)
			req = MeasureInboundBody(req)
			_, _ = io.ReadAll(req.Body)
			require.Nil(t, InboundBodySize(req.Context()))
		})
	}
}
