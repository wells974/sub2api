package service

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"go.uber.org/zap"
)

func excelBPSOnDemandCatalogEnabled(ctx context.Context) (bool, error) {
	raw := os.Getenv("SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED")
	if raw == "" {
		return false, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid SUB2API_BPS_ON_DEMAND_CATALOG_ENABLED")
	}
	return enabled && !isControlledExperiment(ctx), nil
}

func prepareExcelBPSOnDemandCatalog(ctx context.Context, bridge *basispoints.Bridge, prepared, source []byte, enabled bool) ([]byte, error) {
	body, observation, err := bridge.ApplyOnDemandCatalog(prepared, source, enabled)
	if enabled {
		logger.FromContext(ctx).Info("bps_tool_catalog", zap.Any("catalog", observation))
	}
	return body, err
}
