package service

import "encoding/json"

// MarshalPublicSettingsPresentation is shared by the public API DTO and the
// HTML bootstrap payload. Administrative Settings serialization stays intact.
// Pass an alias type without MarshalJSON to avoid recursion.
func MarshalPublicSettingsPresentation(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || !excelBPSNeutralErrors(nil) {
		return encoded, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	if enabled, ok := fields["excel_bps_enabled"]; ok {
		fields["tool_bridge_enabled"] = enabled
		delete(fields, "excel_bps_enabled")
	}
	return json.Marshal(fields)
}

func (p PublicSettingsInjectionPayload) MarshalJSON() ([]byte, error) {
	type plain PublicSettingsInjectionPayload
	return MarshalPublicSettingsPresentation(plain(p))
}
