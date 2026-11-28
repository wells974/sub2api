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
