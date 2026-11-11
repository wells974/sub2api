import type { PublicSettings } from '@/types'

// Accept both deployment versions while retaining existing admin feature
// consumers. Never mutate the API/bootstrap object supplied by the caller.
export function normalizePublicSettings(
  settings: PublicSettings & { tool_bridge_enabled?: boolean }
): PublicSettings {
  if (typeof settings.tool_bridge_enabled !== 'boolean') return settings
  return { ...settings, excel_bps_enabled: settings.tool_bridge_enabled }
}
