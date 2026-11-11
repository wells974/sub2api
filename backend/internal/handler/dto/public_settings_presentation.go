package dto

import "github.com/Wei-Shaw/sub2api/internal/service"

func (p PublicSettings) MarshalJSON() ([]byte, error) {
	type plain PublicSettings
	return service.MarshalPublicSettingsPresentation(plain(p))
}
