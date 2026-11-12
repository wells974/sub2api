package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"go.uber.org/zap"
)

// This observer is deliberately separate from accounting, routing and the
// existing FirstTokenMs definition. All offsets use the forward start clock.
func beginExcelBPSLatency(ctx context.Context, start time.Time) (context.Context, *basispoints.LatencyObservation) {
	return basispoints.WithLatencyObservation(ctx, start)
}

func logExcelBPSLatency(ctx context.Context, o *basispoints.LatencyObservation, accountID int64, stream, failed bool) {
	logger.FromContext(ctx).Info("bps_latency_diagnostics",
		zap.Int("diagnostics_version", 1), zap.Int64("account_id", accountID),
		zap.Bool("stream", stream), zap.Bool("forward_failed", failed),
		zap.Any("latency", o.Snapshot()))
}
