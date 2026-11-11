import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { normalizePublicSettings } from '../publicSettingsPresentation'
import { useAppStore } from '@/stores/app'
import { getPublicSettings } from '@/api/auth'
import type { PublicSettings } from '@/types'

vi.mock('@/api/auth', () => ({ getPublicSettings: vi.fn() }))
vi.mock('@/api/admin/system', () => ({ checkUpdates: vi.fn() }))

describe('public settings transport compatibility', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    delete window.__APP_CONFIG__
    vi.clearAllMocks()
  })

  for (const enabled of [true, false]) {
    for (const source of ['bootstrap', 'api']) {
      it(`preserves ${enabled} from ${source} for existing admin consumers`, async () => {
        const wire = { tool_bridge_enabled: enabled, site_name: 'Site' } as unknown as PublicSettings
        if (source === 'bootstrap') window.__APP_CONFIG__ = wire
        else vi.mocked(getPublicSettings).mockResolvedValue(normalizePublicSettings(wire))
        const app = useAppStore()
        const settings = await app.fetchPublicSettings()
        expect(settings?.excel_bps_enabled).toBe(enabled)
        expect(app.cachedPublicSettings?.excel_bps_enabled).toBe(enabled)
        expect(app.siteName).toBe('Site')
        expect(wire).not.toHaveProperty('excel_bps_enabled')
        expect((await app.fetchPublicSettings())?.excel_bps_enabled).toBe(enabled)
      })
    }
  }

  it('accepts legacy and missing flags without changing the existing opt-out default', () => {
    for (const wire of [{ excel_bps_enabled: false }, { excel_bps_enabled: true }, {}]) {
      expect(normalizePublicSettings(wire as PublicSettings)).toEqual(wire)
    }
  })
})
