package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"time"
)

func (r *settingRepository) CompareAndSetQualityJevConfig(ctx context.Context, previous, value string) error {
	count, err := r.client.Setting.Update().
		Where(setting.KeyEQ(service.SettingKeyQualityJev), setting.ValueEQ(previous)).
		SetValue(value).SetUpdatedAt(time.Now()).Save(ctx)
	if err != nil {
		return err
	}
	if count != 1 {
		return service.ErrQualityJevConflict
	}
	return nil
}
