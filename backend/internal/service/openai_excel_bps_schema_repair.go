package service

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"go.uber.org/zap"
)

// Deployment knobs stay outside upstream settings DTOs and frontend forms.
func excelBPSSchemaRepairContext(ctx context.Context) (context.Context, error) {
	raw := os.Getenv("SUB2API_BPS_SCHEMA_REPAIR_ENABLED")
	if raw == "" {
		return ctx, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return ctx, fmt.Errorf("invalid SUB2API_BPS_SCHEMA_REPAIR_ENABLED")
	}
	if !enabled || isControlledExperiment(ctx) {
		return ctx, nil
	}
	seconds := 30
	if raw := os.Getenv("SUB2API_BPS_SCHEMA_REPAIR_TIMEOUT_SECONDS"); raw != "" {
		seconds, err = strconv.Atoi(raw)
		if err != nil || seconds < 1 || seconds > 120 {
			return ctx, fmt.Errorf("invalid SUB2API_BPS_SCHEMA_REPAIR_TIMEOUT_SECONDS")
		}
	}
	return basispoints.WithSchemaRepair(ctx, time.Duration(seconds)*time.Second), nil
}

// Runs after stream cancellation/close but before the existing compact usage
// defer. The normal terminal already contains aggregate usage: never add it twice.
func (s *OpenAIGatewayService) finishExcelBPSSchemaRepair(ctx context.Context, result *OpenAIForwardResult) {
	observation, usage := basispoints.SchemaRepairSnapshot(ctx)
	if !observation.Considered {
		return
	}
	if result != nil && result.UpstreamTerminalEvent == "" && len(usage) > 0 {
		s.parseSSEUsageBytes(usage, &result.Usage)
		result.Usage.CacheReadKnown = false
	}
	logger.FromContext(ctx).Info("bps_schema_repair", zap.Any("schema_repair", observation))
}
