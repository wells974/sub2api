package service

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"go.uber.org/zap"
)

// Keep the gateway hook small: this observer owns only bounded metadata and
// never the request body, response body, credentials, or validator errors.
type excelBPSDiagnostics struct {
	images basispoints.ImageObservation
	schema *basispoints.ToolSchemaValidationError
}

func newExcelBPSDiagnostics(body []byte) *excelBPSDiagnostics {
	return &excelBPSDiagnostics{images: basispoints.ObserveRequestImages(body)}
}

func (d *excelBPSDiagnostics) observePreparationError(err error) {
	var schema *basispoints.ToolSchemaValidationError
	if errors.As(err, &schema) {
		copy := *schema
		d.schema = &copy
	}
}

func (d *excelBPSDiagnostics) log(ctx context.Context, accountID int64, bridge *basispoints.Bridge, forwardFailed bool) {
	observation := bridge.Observations()
	fields := []zap.Field{
		zap.Int("diagnostics_version", 1),
		zap.Int64("account_id", accountID),
		zap.Bool("forward_failed", forwardFailed),
		zap.Any("input_images", d.images),
		zap.Bool("catalog_prepared", bridge != nil),
		zap.Any("catalog", observation.Catalog),
	}
	if d.schema != nil {
		fields = append(fields, zap.String("schema_phase", "catalog_compile"),
			zap.String("schema_tool", d.schema.Tool), zap.String("schema_path", d.schema.Path),
			zap.String("schema_reason", d.schema.Reason))
	}
	if observation.ArgumentFailure != nil {
		fields = append(fields, zap.String("schema_phase", "tool_arguments"),
			zap.Any("schema_failure", observation.ArgumentFailure))
	}
	logger.FromContext(ctx).Info("bps_request_diagnostics", fields...)
}
