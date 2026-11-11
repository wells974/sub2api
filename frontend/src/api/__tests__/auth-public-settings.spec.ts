import { describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))

describe('public settings API compatibility', () => {
  for (const enabled of [true, false]) {
    it(`normalizes the neutral ${enabled} flag without mutating wire data`, async () => {
      const { getPublicSettings } = await import('@/api/auth')
      const wire = { tool_bridge_enabled: enabled, site_name: 'Site' }
      get.mockResolvedValue({ data: wire })
      const settings = await getPublicSettings()
      expect(get).toHaveBeenCalledWith('/settings/public')
      expect(settings.excel_bps_enabled).toBe(enabled)
      expect(settings.site_name).toBe('Site')
      expect(wire).not.toHaveProperty('excel_bps_enabled')
    })
  }
})
