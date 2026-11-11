package service

import (
	"bytes"
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const excelBPSOpsPresentationKey = "excel_bps_ops_error_presentation"

type excelBPSOpsPresentation struct {
	original []byte
	public   []byte
}

// Retain only the small error object, never the request or a full response.
// This is an output-boundary receipt, not a flag that an earlier attempt used BPS.
func rememberExcelBPSOpsPresentation(c *gin.Context, original, public gin.H) {
	if c == nil {
		return
	}
	raw, err := json.Marshal(original)
	if err != nil || len(raw) > 64<<10 {
		return
	}
	wire, err := json.Marshal(public)
	if err != nil {
		return
	}
	c.Set(excelBPSOpsPresentationKey, excelBPSOpsPresentation{original: raw, public: wire})
}

// RestoreOpsErrorPresentation provides the pre-presentation error to the
// existing classifier/storage redactor, after the client response is finished.
// It never writes to the client, changes HTTP evidence, or touches Ops markers.
func RestoreOpsErrorPresentation(c *gin.Context, captured []byte) []byte {
	if c == nil {
		return captured
	}
	v, exists := c.Get(excelBPSOpsPresentationKey)
	receipt, ok := v.(excelBPSOpsPresentation)
	if !exists || !ok {
		return captured
	}
	restore := func(payload []byte) []byte {
		if !json.Valid(payload) {
			return payload
		}
		for _, path := range []string{"error", "response.error"} {
			detail := gjson.GetBytes(payload, path)
			if !detail.IsObject() {
				continue
			}
			matches := true
			for _, key := range []string{"type", "code", "message"} {
				matches = matches && detail.Get(key).String() == gjson.GetBytes(receipt.public, key).String()
			}
			if matches {
				if next, err := sjson.SetRawBytes(payload, path, receipt.original); err == nil {
					payload = next
				}
			}
		}
		return payload
	}
	if json.Valid(captured) {
		return restore(captured)
	}
	lines := bytes.Split(captured, []byte("\n"))
	for i, line := range lines {
		if bytes.HasPrefix(line, []byte("data: ")) {
			lines[i] = append([]byte("data: "), restore(line[6:])...)
		}
	}
	return bytes.Join(lines, []byte("\n"))
}
