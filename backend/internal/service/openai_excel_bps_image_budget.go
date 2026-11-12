package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Deployment-level options are isolated from the upstream admin settings DTOs.
// Existing relay limits never implicitly raise native limits on upgrade.
type excelBudgetOptions struct {
	NativeTotalMiB int
	ByteCompact    bool
	TrimOldTools   bool
	MessageImages  bool
}

// Extension settings/state do not change upstream's DTO or legacy policy. The
// default path delegates to that implementation, including its error messages.
type excelImageBudgetSettings struct {
	ExcelBPSImageRelaySettings
	BudgetOptions excelBudgetOptions
}

type excelImageBudgetState struct {
	*excelImagePolicyState
	extended bool
}

func (settings excelImageBudgetSettings) extendedBudget() bool {
	options := settings.BudgetOptions
	return options.MessageImages || options.ByteCompact || options.TrimOldTools || (settings.Mode == ExcelBPSImageModeNative && options.NativeTotalMiB != 0 && options.NativeTotalMiB != 32)
}

func (s *OpenAIGatewayService) prepareExcelImagePolicyWithBudget(ctx context.Context, c *gin.Context, body []byte, settings excelImageBudgetSettings, scope string, trusted bool) (*excelImageBudgetState, error) {
	extended := settings.extendedBudget()
	if !extended && settings.Enabled && trusted {
		// Existing v2 checkpoints must remain recognizable after disabling new
		// options. This optional probe must not create a new cache dependency for
		// legacy requests: on absence/error, run the unchanged legacy policy.
		if cache, ok := s.cache.(ExcelBPSImagePolicyCache); ok {
			previous, err := cache.LoadBPSImageProgress(ctx, scope+"/image-history:v2")
			extended = err == nil && previous != ""
		}
	}
	var state *excelImagePolicyState
	var err error
	if extended {
		state, err = s.prepareExcelImageBudgetPolicy(ctx, c, body, settings, scope, trusted)
	} else {
		state, err = s.prepareExcelImagePolicy(ctx, c, body, settings.ExcelBPSImageRelaySettings, scope, trusted)
	}
	if err != nil {
		return nil, err
	}
	return &excelImageBudgetState{excelImagePolicyState: state, extended: extended}, nil
}

func (p *excelImageBudgetState) reconcileBody(body []byte, settings excelImageBudgetSettings) ([]byte, error) {
	if len(p.reconciledBody) == 0 {
		return body, nil
	}
	if !p.extended {
		// Upstream's legacy checkpoint path checks the original aggregate first.
		budget := excelImageBudget(settings)
		if err := basispoints.ValidateImageBudget(body, int(budget.MaxImageBytes>>20), int(budget.MaxTotalBytes>>20)); err != nil {
			return nil, err
		}
	}
	return p.reconciledBody, nil
}

func (s *OpenAIGatewayService) excelBPSImageSettings(ctx context.Context) (excelImageBudgetSettings, error) {
	base, err := s.settingService.GetExcelBPSImageRelaySettings(ctx)
	settings := excelImageBudgetSettings{ExcelBPSImageRelaySettings: base}
	if err != nil {
		return settings, err
	}
	settings.BudgetOptions, err = readExcelBudgetOptions(os.LookupEnv)
	return settings, err
}

func readExcelBudgetOptions(lookup func(string) (string, bool)) (excelBudgetOptions, error) {
	options := excelBudgetOptions{NativeTotalMiB: 32}
	if raw, exists := lookup("SUB2API_BPS_NATIVE_IMAGE_TOTAL_MIB"); exists {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > basispoints.MaxRelayRequestMiB {
			return options, fmt.Errorf("invalid SUB2API_BPS_NATIVE_IMAGE_TOTAL_MIB")
		}
		options.NativeTotalMiB = value
	}
	for key, target := range map[string]*bool{"SUB2API_BPS_IMAGE_BYTE_COMPACT": &options.ByteCompact, "SUB2API_BPS_TRIM_OLD_TOOL_IMAGES": &options.TrimOldTools} {
		if raw, exists := lookup(key); exists {
			value, err := strconv.ParseBool(raw)
			if err != nil {
				return options, fmt.Errorf("invalid %s", key)
			}
			*target = value
		}
	}
	var err error
	options.MessageImages, err = readExcelMessageImageOption(lookup)
	return options, err
}

func excelImageBudget(settings excelImageBudgetSettings) basispoints.ImageBudget {
	imageMiB, totalMiB := settings.Limits.MaxImageMiB, settings.Limits.MaxTotalMiB
	if settings.Mode == ExcelBPSImageModeNative {
		imageMiB, totalMiB = 20, settings.BudgetOptions.NativeTotalMiB
		if totalMiB == 0 {
			totalMiB = 32
		}
	}
	return basispoints.ImageBudget{MaxImages: settings.Limits.MaxImages, MaxImageBytes: int64(imageMiB) << 20, MaxTotalBytes: int64(totalMiB) << 20}
}

func byteCompactNeeded(h *basispoints.ImageHistory, settings excelImageBudgetSettings) bool {
	return settings.Policy == "auto_compact" && settings.BudgetOptions.ByteCompact && basispoints.MeasureImageInput(h.Input).Bytes >= excelImageBudget(settings).MaxTotalBytes*3/4
}

func imagePolicyStateNeeded(h *basispoints.ImageHistory, settings excelImageBudgetSettings) bool {
	if settings.Policy == "off" || settings.Policy == "" {
		return false
	}
	if settings.Policy == "auto_compact" {
		if split, err := h.Split(nil); err == nil && split == 0 && excelImageBudget(settings).CheckKnown(basispoints.MeasureImageInput(h.Input)) == nil {
			return false
		}
		return h.Count > settings.Limits.MaxImages || byteCompactNeeded(h, settings)
	}
	return h.Count >= settings.Limits.MaxImages-settings.WarningRemaining
}

// Stable checkpoint identity excludes all mutable policy settings. Read the
// current legacy key once on upgrade and migrate via CAS, leaving it untouched.
func loadImagePolicyState(ctx context.Context, cache ExcelBPSImagePolicyCache, scope string, settings excelImageBudgetSettings) (string, string, error) {
	stable := scope + "/image-history:v2"
	previous, err := cache.LoadBPSImageProgress(ctx, stable)
	if err != nil || previous != "" {
		return stable, previous, err
	}
	policies := []string{settings.Policy, "auto_compact", "warn"}
	for _, policy := range policies {
		legacy := fmt.Sprintf("%s/policy:%s/limit:%d/warn:%d/reserve:%d", scope, policy, settings.Limits.MaxImages, settings.WarningRemaining, settings.CompactReserve)
		value, e := cache.LoadBPSImageProgress(ctx, legacy)
		if e != nil {
			return stable, "", e
		}
		if value == "" {
			continue
		}
		if e = cache.SaveBPSImageProgress(ctx, stable, "", value); e != nil {
			// A concurrent migration is safe only if a winner is now readable.
			current, readErr := cache.LoadBPSImageProgress(ctx, stable)
			if readErr != nil || current == "" {
				return stable, "", e
			}
			return stable, current, nil
		}
		return stable, value, nil
	}
	return stable, "", nil
}

func (p *excelImagePolicyState) planImagePartitions(ctx context.Context, split int, settings excelImageBudgetSettings) error {
	budget := excelImageBudget(settings)
	tail := basispoints.MeasureImageInput(p.history.Input[split:])
	if err := budget.CheckKnown(tail); err != nil {
		return imagePolicyError(400, "basispoints_image_batch_too_large", "New images exceed the per-request budget; all new images were preserved: "+err.Error())
	}
	if split == 0 {
		// An early threshold is not a hard limit. A first turn that fits the
		// actual budget has no history to compact and can proceed unchanged.
		return nil
	}
	prefix := basispoints.MeasureImageInput(p.history.Input[:split])
	if budget.CheckKnown(prefix) != nil && settings.BudgetOptions.TrimOldTools {
		if len(p.checkpoints) >= 16 {
			return imagePolicyError(400, "basispoints_image_checkpoint_limit", "Run compact manually to reset the image history checkpoint chain")
		}
		target := budget
		target.MaxTotalBytes = budget.MaxTotalBytes * 3 / 4
		next, cp, err := p.history.TrimOldToolImages(split, target)
		if err != nil {
			return imagePolicyError(400, "basispoints_image_history_limit", err.Error())
		}
		if err = budget.CheckKnown(basispoints.MeasureImageInput(next.Input[:split])); err != nil {
			return imagePolicyError(400, "basispoints_image_history_limit", "Protected history still exceeds the compaction budget: "+err.Error())
		}
		needsCompact := budget.CheckKnown(basispoints.MeasureImageInput(next.Input)) != nil || byteCompactNeeded(next, settings)
		if needsCompact && len(p.checkpoints) >= 15 {
			return imagePolicyError(400, "basispoints_image_checkpoint_limit", "Not enough checkpoint capacity for trimming and compaction; run compact manually")
		}
		// Persist before using the transformed history, even if generation fails.
		checkpoints := append(append([]basispoints.ImageCheckpoint{}, p.checkpoints...), cp)
		raw, err := json.Marshal(excelImagePolicyCacheState{Progress: next.Progress(), Checkpoints: checkpoints})
		if err != nil {
			return err
		}
		if err = p.cache.SaveBPSImageProgress(ctx, p.scope, p.previous, string(raw)); err != nil {
			return imagePolicyError(503, "basispoints_image_checkpoint_unavailable", "Could not save screenshot trimming safely; retry with the same conversation ID")
		}
		p.previous, p.checkpoints, p.history = string(raw), checkpoints, next
		p.reconciledBody, err = next.Body()
		if err != nil {
			return err
		}
		logger.FromContext(ctx).Info("bps_image_history_trim", zap.Int("removed_images", len(cp.Trim)), zap.Int64("remaining_bytes", basispoints.MeasureImageInput(next.Input).Bytes))
		if !needsCompact {
			return nil
		}
	}
	if err := budget.CheckKnown(basispoints.MeasureImageInput(p.history.Input[:split])); err != nil {
		return imagePolicyError(400, "basispoints_image_history_limit", "Historical prefix exceeds the compaction budget; enable optional old tool screenshot trimming or compact manually: "+err.Error())
	}
	if len(p.checkpoints) >= 16 {
		return imagePolicyError(400, "basispoints_image_checkpoint_limit", "Run compact manually to reset the image history checkpoint chain")
	}
	p.split = split
	p.maxImages = p.history.Count
	logger.FromContext(ctx).Info("bps_image_budget_compaction", zap.Int64("history_bytes", basispoints.MeasureImageInput(p.history.Input[:split]).Bytes), zap.Int64("new_bytes", tail.Bytes), zap.Int64("window_budget_bytes", budget.MaxTotalBytes))
	return nil
}

// The concatenated upload plan may cover two individually validated requests.
// Raw body and concurrent memory admission still happen before this point.
func (p *excelImageBudgetState) uploadTotalMiB(settings excelImageBudgetSettings) int {
	total := int(excelImageBudget(settings).MaxTotalBytes >> 20)
	if p.extended && p.split > 0 {
		total *= 2
	}
	return total
}

func (p *excelImageBudgetState) validateContinuation(window []any, settings excelImageBudgetSettings) error {
	if !p.extended {
		if basispoints.CountInlineImages(window)+basispoints.CountInlineImages(p.history.Input[p.split:]) > settings.Limits.MaxImages {
			return fmt.Errorf("Compacted history still exceeds the image limit; reduce new images or compact manually")
		}
		return nil
	}
	input := append(append([]any{}, window...), p.history.Input[p.split:]...)
	if err := excelImageBudget(settings).Check(input); err != nil {
		return fmt.Errorf("Compacted history still exceeds the image budget; reduce new images or compact manually: %w", err)
	}
	return nil
}

func (s *OpenAIGatewayService) prepareExcelImageBudgetPolicy(ctx context.Context, c *gin.Context, body []byte, settings excelImageBudgetSettings, scope string, trusted bool) (*excelImagePolicyState, error) {
	state := &excelImagePolicyState{maxImages: settings.Limits.MaxImages}
	if !settings.Enabled {
		return state, nil
	}
	history, err := basispoints.InspectImageHistory(body)
	if err != nil {
		return nil, err
	}
	state.history = history
	state.compact = isOpenAIResponsesCompactPath(c) || HasCompactionTriggerInInput(body)
	cache, ok := s.cache.(ExcelBPSImagePolicyCache)
	if !ok || !trusted {
		if state.compact || !imagePolicyStateNeeded(history, settings) {
			return state, nil
		}
		return nil, imagePolicyError(503, "basispoints_image_policy_state_unavailable", "Image policy requires a stable conversation identity and available session state; retry with the same conversation ID")
	}
	state.cache = cache
	state.scope, state.previous, err = loadImagePolicyState(ctx, cache, scope, settings)
	if err != nil {
		if state.compact {
			return state, nil
		}
		return nil, imagePolicyError(503, "basispoints_image_policy_state_unavailable", "Image policy session state is unavailable; retry later")
	}
	var cached excelImagePolicyCacheState
	if state.previous != "" {
		if err := json.Unmarshal([]byte(state.previous), &cached); err != nil {
			return nil, imagePolicyError(503, "basispoints_image_policy_state_unavailable", "Invalid image session state; run compact manually")
		}
	}
	state.checkpoints = cached.Checkpoints
	if len(cached.Checkpoints) > 0 {
		changed, e := reconcileExcelMessageImages(ctx, scope, history, cached.Checkpoints)
		if e != nil {
			return nil, imagePolicyError(400, "basispoints_image_history_boundary", e.Error())
		}
		if changed {
			state.reconciledBody, err = history.Body()
			if err != nil {
				return nil, err
			}
		}
	}
	if err := s.prepareExcelMessageImages(ctx, state, settings, body, scope); err != nil {
		return nil, err
	}
	history = state.history
	n := history.Count
	limit := settings.Limits.MaxImages
	if state.compact || settings.Policy == "off" || settings.Policy == "" {
		return state, nil
	} // Ordinary and warning thresholds never block explicit compact.
	if settings.Policy == "warn" {
		if n > limit-settings.CompactReserve {
			return nil, imagePolicyError(400, "basispoints_image_limit_reached", "Image limit reached. Run compact before adding more images. 图片空间不足，请先执行 compact 压缩。")
		}
		if n >= limit-settings.WarningRemaining {
			first, e := cache.ClaimBPSImageWarning(ctx, state.scope)
			if e != nil {
				return nil, imagePolicyError(503, "basispoints_image_policy_state_unavailable", "Image policy session state is unavailable; retry later")
			}
			if first {
				remaining := limit - settings.CompactReserve - n
				return nil, imagePolicyError(400, "basispoints_image_compact_recommended", fmt.Sprintf("按当前请求计算，还可新增 %d 张图片，建议先执行 compact 压缩；直接重试可继续。Based on this request, %d additional image slots remain; run compact or retry to continue.", remaining, remaining))
			}
		}
		return state, nil
	}
	if n <= limit && !byteCompactNeeded(history, settings) && !messageImageBudgetExceeded(history, settings) {
		return state, nil
	}
	var progress *basispoints.ImageProgress
	if state.previous != "" {
		var p basispoints.ImageProgress
		if json.Unmarshal([]byte(state.previous), &p) == nil && p.Digest != "" {
			progress = &p
		} else {
			var cached excelImagePolicyCacheState
			if json.Unmarshal([]byte(state.previous), &cached) == nil && cached.Progress.Digest != "" {
				progress = &cached.Progress
			}
		}
	}
	split, err := splitExcelMessageHistory(history, progress, state.messageImageBoundary)
	if err != nil {
		return nil, imagePolicyError(400, "basispoints_image_history_boundary", err.Error())
	}
	if split > 0 && !isSupportedImagePolicyCodex(c) {
		return nil, imagePolicyError(400, "basispoints_image_auto_compact_unsupported", "Automatic image compaction requires a Codex client; run compact manually")
	}
	if err := state.planImagePartitions(ctx, split, settings); err != nil {
		return nil, err
	}
	return state, nil
}
