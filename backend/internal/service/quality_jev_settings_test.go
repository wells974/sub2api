package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type jevSettingsTestRepo struct {
	contentModerationTestSettingRepo
}

func (r *jevSettingsTestRepo) CompareAndSetQualityJevConfig(ctx context.Context, old, next string) error {
	if r.values[SettingKeyQualityJev] != old {
		return ErrQualityJevConflict
	}
	return r.Set(ctx, SettingKeyQualityJev, next)
}

func TestQualityJevSettingsIndependentLifecycle(t *testing.T) {
	repo := &jevSettingsTestRepo{contentModerationTestSettingRepo{values: map[string]string{
		SettingKeyQualityJev:              `{"enabled":false,"typesafe":{}}`,
		SettingKeyContentModerationConfig: `{"enabled":false,"mode":"off"}`,
	}}}
	service := &SettingService{settingRepo: repo}
	ctx := t.Context()
	initial, err := service.GetQualityJevSettings(ctx)
	require.NoError(t, err)
	require.False(t, initial.Enabled)
	initial.Enabled = true
	require.Error(t, service.SaveQualityJevSettings(ctx, *initial))
	initial.Keys = []QualityJevKey{{Key: "synthetic-full-secret", BaseURL: "https://relay.example/v1"}}
	require.NoError(t, service.SaveQualityJevSettings(ctx, *initial))
	require.ErrorIs(t, service.SaveQualityJevSettings(ctx, *initial), ErrQualityJevConflict)
	current, err := service.GetQualityJevSettings(ctx)
	require.NoError(t, err)
	data, err := json.Marshal(current)
	require.NoError(t, err)
	require.NotContains(t, string(data), "synthetic-full-secret")
	require.Len(t, current.Keys, 1)
	require.Equal(t, initial.Keys[0].BaseURL, current.Keys[0].BaseURL)
	current.Enabled = false
	current.BaseURL = "https://new.example"
	require.NoError(t, service.SaveQualityJevSettings(ctx, *current))
	saved, _, err := loadQualityJevSettings(ctx, repo)
	require.NoError(t, err)
	require.False(t, saved.Enabled)
	require.Equal(t, []string{"synthetic-full-secret"}, saved.TypeSafe.APIKeys)
	require.Equal(t, `{"enabled":false,"mode":"off"}`, repo.values[SettingKeyContentModerationConfig])
}
