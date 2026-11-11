package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExcelBPSPublicSettingsKeepAdminFields(t *testing.T) {
	t.Setenv("SUB2API_BPS_ERROR_PRESENTATION", "")
	admin, err := json.Marshal(SystemSettings{ExcelBPSEnabled: true})
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(admin, "excel_bps_enabled").Bool())
	public, err := json.Marshal(PublicSettings{ExcelBPSEnabled: true})
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(public, "excel_bps_enabled").Exists())
	require.True(t, gjson.GetBytes(public, "tool_bridge_enabled").Bool())
}
