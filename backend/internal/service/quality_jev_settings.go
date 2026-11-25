package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/typesafe"
)

const SettingKeyQualityJev = "quality_jev_config"

type qualityJevSettings struct {
	Enabled  bool                  `json:"enabled"`
	TypeSafe *qualityJevConnection `json:"typesafe"`
}

type qualityJevConnection struct {
	BaseURL        string            `json:"base_url"`
	Model          string            `json:"model"`
	APIKeys        []string          `json:"api_keys,omitempty"`
	APIKeyBaseURLs map[string]string `json:"api_key_base_urls,omitempty"`
	ProxyID        *int64            `json:"proxy_id,omitempty"`
	TimeoutMS      int               `json:"timeout_ms"`
	RetryCount     int               `json:"retry_count"`
}

func loadQualityJevSettings(ctx context.Context, repo SettingRepository) (qualityJevSettings, string, error) {
	cfg := qualityJevSettings{TypeSafe: &qualityJevConnection{RetryCount: 2}}
	raw, err := repo.GetValue(ctx, SettingKeyQualityJev)
	if err != nil {
		return cfg, "", err
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil || cfg.TypeSafe == nil {
		return cfg, "", infraerrors.InternalServer("QUALITY_JEV_INVALID_SETTINGS", "Could not read Jev settings")
	}
	if strings.TrimSpace(cfg.TypeSafe.BaseURL) == "" {
		cfg.TypeSafe.BaseURL = typesafe.DefaultBaseURL
	}
	cfg.TypeSafe.Model = strings.TrimSpace(cfg.TypeSafe.Model)
	if cfg.TypeSafe.Model == "" {
		cfg.TypeSafe.Model = typesafe.JevLatestModel
	}
	if cfg.TypeSafe.TimeoutMS <= 0 {
		cfg.TypeSafe.TimeoutMS = 3000
	}
	cfg.TypeSafe.TimeoutMS = min(cfg.TypeSafe.TimeoutMS, 30000)
	cfg.TypeSafe.RetryCount = min(max(cfg.TypeSafe.RetryCount, 0), 2)
	return cfg, raw, nil
}

type QualityJevKey struct {
	ID      string `json:"id,omitempty"`
	Masked  string `json:"masked,omitempty"`
	Key     string `json:"key,omitempty"` // Write only for a new credential.
	BaseURL string `json:"base_url"`
}

type QualityJevSettingsView struct {
	Revision   string          `json:"revision" binding:"required"`
	Enabled    bool            `json:"enabled"`
	BaseURL    string          `json:"base_url"`
	ProxyID    *int64          `json:"proxy_id"`
	TimeoutMS  int             `json:"timeout_ms"`
	RetryCount int             `json:"retry_count"`
	Keys       []QualityJevKey `json:"keys"`
}

func jevSettingsRevision(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func (s *SettingService) GetQualityJevSettings(ctx context.Context) (*QualityJevSettingsView, error) {
	cfg, raw, err := loadQualityJevSettings(ctx, s.settingRepo)
	if err != nil {
		return nil, err
	}
	p := cfg.TypeSafe
	view := &QualityJevSettingsView{
		Revision: jevSettingsRevision(raw), Enabled: cfg.Enabled,
		BaseURL: p.BaseURL, ProxyID: p.ProxyID, TimeoutMS: p.TimeoutMS, RetryCount: p.RetryCount,
		Keys: make([]QualityJevKey, 0, len(p.APIKeys)),
	}
	for _, key := range p.APIKeys {
		id := scopedModerationKeyHash(key, ContentModerationEngineTypeSafe)
		view.Keys = append(view.Keys, QualityJevKey{ID: id, Masked: maskSecretTail(key), BaseURL: p.APIKeyBaseURLs[id]})
	}
	return view, nil
}

var ErrQualityJevConflict = infraerrors.Conflict("QUALITY_JEV_CHANGED", "Jev settings changed. Reopen settings and try again.")

func (s *SettingService) SaveQualityJevSettings(ctx context.Context, input QualityJevSettingsView) error {
	cfg, raw, err := loadQualityJevSettings(ctx, s.settingRepo)
	if err != nil {
		return err
	}
	if input.Revision != jevSettingsRevision(raw) {
		return ErrQualityJevConflict
	}
	base := strings.TrimSpace(input.BaseURL)
	if base == "" {
		base = typesafe.DefaultBaseURL
	}
	if _, err := typesafe.SystemOneURL(base); err != nil {
		return infraerrors.BadRequest("QUALITY_JEV_URL", "Invalid Jev endpoint")
	}
	if input.TimeoutMS < 1 || input.TimeoutMS > 30000 || input.RetryCount < 0 || input.RetryCount > 2 || (input.ProxyID != nil && *input.ProxyID <= 0) {
		return infraerrors.BadRequest("QUALITY_JEV_CONNECTION", "Invalid Jev connection settings")
	}
	known := make(map[string]string, len(cfg.TypeSafe.APIKeys))
	for _, key := range cfg.TypeSafe.APIKeys {
		known[scopedModerationKeyHash(key, ContentModerationEngineTypeSafe)] = key
	}
	p := &qualityJevConnection{
		BaseURL: base, Model: typesafe.JevLatestModel,
		ProxyID: input.ProxyID, TimeoutMS: input.TimeoutMS, RetryCount: input.RetryCount,
		APIKeyBaseURLs: make(map[string]string),
	}
	seen := make(map[string]bool)
	for _, row := range input.Keys {
		key := strings.TrimSpace(row.Key)
		if row.ID != "" {
			if key != "" || known[row.ID] == "" {
				return ErrQualityJevConflict
			}
			key = known[row.ID]
		}
		if key == "" || strings.ContainsAny(key, "\r\n") || seen[key] {
			return infraerrors.BadRequest("QUALITY_JEV_KEY", "Empty or duplicate Jev key")
		}
		seen[key] = true
		p.APIKeys = append(p.APIKeys, key)
		if override := strings.TrimSpace(row.BaseURL); override != "" {
			if _, err := typesafe.SystemOneURL(override); err != nil {
				return infraerrors.BadRequest("QUALITY_JEV_URL", "Invalid per-key Jev endpoint")
			}
			p.APIKeyBaseURLs[scopedModerationKeyHash(key, ContentModerationEngineTypeSafe)] = override
		}
	}
	if input.Enabled && len(p.APIKeys) == 0 {
		return infraerrors.BadRequest("QUALITY_JEV_KEY_REQUIRED", "Add a TypeSafe key to enable Jev grading")
	}
	value, err := json.Marshal(qualityJevSettings{Enabled: input.Enabled, TypeSafe: p})
	if err != nil {
		return err
	}
	writer, ok := s.settingRepo.(interface {
		CompareAndSetQualityJevConfig(context.Context, string, string) error
	})
	if !ok {
		return infraerrors.InternalServer("QUALITY_JEV_STORAGE", "Cannot save Jev settings")
	}
	return writer.CompareAndSetQualityJevConfig(ctx, raw, string(value))
}
