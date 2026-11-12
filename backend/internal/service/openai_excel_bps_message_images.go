package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"go.uber.org/zap"
)

// Isolated process-local derived-image cache; no images enter Redis or disk.
var excelMessageImageCache basispoints.MessageImageCache

func messageImageBudgetExceeded(history *basispoints.ImageHistory, settings excelImageBudgetSettings) bool {
	if !settings.BudgetOptions.MessageImages {
		return false
	}
	usage, budget := basispoints.MeasureImageInput(history.Input), excelImageBudget(settings)
	return usage.Bytes > budget.MaxTotalBytes || usage.MaxBytes > budget.MaxImageBytes
}

func splitExcelMessageHistory(history *basispoints.ImageHistory, previous *basispoints.ImageProgress, preserved int) (int, error) {
	if preserved > 0 {
		return preserved, nil
	}
	return history.Split(previous)
}

func readExcelMessageImageOption(lookup func(string) (string, bool)) (bool, error) {
	raw, exists := lookup("SUB2API_BPS_MESSAGE_IMAGE_COMPACT")
	if !exists {
		return false, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid SUB2API_BPS_MESSAGE_IMAGE_COMPACT")
	}
	return enabled, nil
}

func reconcileExcelMessageImages(ctx context.Context, scope string, history *basispoints.ImageHistory, checkpoints []basispoints.ImageCheckpoint) (bool, error) {
	hasImages := false
	for _, cp := range checkpoints {
		hasImages = hasImages || len(cp.MessageImages) > 0
	}
	if !hasImages {
		return history.Reconcile(checkpoints)
	}
	work, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	observation := basispoints.MessageImageObservation{BeforeBytes: basispoints.MeasureImageInput(history.Input).Bytes}
	changed, err := excelMessageImageCache.ReconcileObserved(work, scope, history, checkpoints, &observation)
	observation.AfterBytes = basispoints.MeasureImageInput(history.Input).Bytes
	observation.Reason = "replayed"
	if !changed {
		observation.Reason = "prefix_not_matched"
	}
	if err != nil {
		observation.Reason = "replay_failed"
	}
	logger.FromContext(ctx).Info("bps_message_image_replay", zap.Any("images", observation), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
	return changed, err
}

func (s *OpenAIGatewayService) prepareExcelMessageImages(ctx context.Context, state *excelImagePolicyState, settings excelImageBudgetSettings, originalBody []byte, scope string) error {
	if !settings.BudgetOptions.MessageImages || settings.Policy != "auto_compact" || state.compact || state.cache == nil {
		return nil
	}
	budget := excelImageBudget(settings)
	usage := basispoints.MeasureImageInput(state.history.Input)
	if usage.Bytes <= budget.MaxTotalBytes && usage.MaxBytes <= budget.MaxImageBytes {
		return nil
	}
	start := time.Now()
	observation := basispoints.MessageImageObservation{BeforeBytes: usage.Bytes, AfterBytes: usage.Bytes}
	defer func() {
		logger.FromContext(ctx).Info("bps_message_image_compaction", zap.Any("images", observation), zap.Int64("duration_ms", time.Since(start).Milliseconds()))
	}()
	var previous basispoints.ImageProgress
	if state.previous != "" {
		var cached excelImagePolicyCacheState
		if json.Unmarshal([]byte(state.previous), &cached) == nil {
			previous = cached.Progress
		}
		if previous.Digest == "" {
			_ = json.Unmarshal([]byte(state.previous), &previous)
		}
	}
	split, reason := basispoints.MessageImageBoundary(state.history, &previous)
	if split <= 0 {
		observation.Reason = reason
		if reason == "ready" {
			observation.Reason = "no_confirmed_history"
		}
		return nil
	}
	work, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	next, checkpoint, observed, err := excelMessageImageCache.CompressHistory(work, scope, state.history, split, budget, state.checkpoints)
	observation = observed
	if err != nil {
		observation.Reason = "processing_stopped"
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Optional optimization exhausted its bounded work window. Preserve the
		// original request and let the existing budget policy decide the result.
		return nil
	}
	if len(checkpoint.MessageImages) == 0 {
		return nil
	}
	observation.Reason = "prepared"
	original, err := basispoints.InspectImageHistory(originalBody)
	if err != nil {
		return err
	}
	checkpoints := excelMessageImageCache.AppendCheckpoint(work, scope, original, next, state.checkpoints, checkpoint)
	if len(checkpoints) >= 16 {
		observation.Reason = "checkpoint_capacity"
		return imagePolicyError(400, "basispoints_image_checkpoint_limit", "Not enough checkpoint capacity for message images and compaction; run compact manually")
	}
	body, err := next.Body()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(excelImagePolicyCacheState{Progress: next.Progress(), Checkpoints: checkpoints})
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		observation.Reason = "cancelled"
		return err
	}
	if work.Err() != nil {
		observation.Reason = "processing_stopped"
		return nil
	}
	if err = state.cache.SaveBPSImageProgress(ctx, state.scope, state.previous, string(raw)); err != nil {
		observation.Reason = "checkpoint_save_failed"
		return imagePolicyError(503, "basispoints_image_checkpoint_unavailable", "Could not save message image compression safely; retry with the same conversation ID")
	}
	state.previous, state.checkpoints, state.history = string(raw), checkpoints, next
	state.messageImageBoundary = split
	state.reconciledBody = body
	observation.Reason = "compressed"
	return nil
}
