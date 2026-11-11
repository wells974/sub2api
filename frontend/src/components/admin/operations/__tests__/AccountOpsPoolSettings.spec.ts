import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import PoolSettings from '../AccountOpsPoolSettings.vue'
import PoolEvent from '../AccountOpsPoolEvent.vue'
import pool from '@/i18n/locales/zh/accountOpsPool'
import { apiClient } from '@/api/client'
import type { AccountOpsConfig } from '@/api/admin/accountOps'

vi.mock('@/api/client', () => ({ apiClient: { put: vi.fn() } }))
const config = (): AccountOpsConfig => ({ enabled: true, recipient: 'ops@example.test', balance_low: true, weekly_quota: true, cooldown_minutes: 60 })
const plugins = () => [createI18n({ legacy: false, locale: 'zh', messages: { zh: { accountOps: { pool } } } })]
const mounted: Array<{ unmount: () => void }> = []
function create(c = config()) {
  const view = mount(PoolSettings, { props: { config: c }, global: { plugins: plugins() } })
  mounted.push(view)
  return view
}
beforeEach(() => { vi.resetAllMocks() })
afterEach(() => { mounted.splice(0).forEach(view => view.unmount()) })

describe('pool notification settings', () => {
  it('defaults to opt-out and saves only the pool extension through the scoped API', async () => {
    const view = create()
    expect((view.get('[data-testid="pool-enabled"]').element as HTMLInputElement).checked).toBe(false)
    expect((view.get('[data-testid="pool-counts"]').element as HTMLInputElement).checked).toBe(true)
    const pool = { enabled: true, notify_states: true, notify_counts: true }
    vi.mocked(apiClient.put).mockResolvedValue({ data: { ...config(), pool_notifications: pool } })
    await view.get('[data-testid="pool-enabled"]').setValue(true)
    await view.get('form').trigger('submit')
    await flushPromises()
    expect(apiClient.put).toHaveBeenCalledOnce()
    expect(apiClient.put).toHaveBeenCalledWith('/admin/account-ops/notification-settings', { pool_notifications: pool })
    expect(view.emitted('saved')?.[0]?.[0]).toMatchObject({ recipient: 'ops@example.test', pool_notifications: pool })
    expect(view.emitted('saving')).toEqual([[true], [false]])
    expect(view.get('code').text()).toContain('"body":"{{message}}"')
    expect(view.get('code').text()).not.toContain('device_key')
  })

  it('preserves unsaved edits across polling and allows retry after an API failure', async () => {
    const view = create()
    await view.get('[data-testid="pool-enabled"]').setValue(true)
    await view.setProps({ config: { ...config(), pool_notifications: { enabled: false, notify_states: false, notify_counts: true } } })
    expect((view.get('[data-testid="pool-enabled"]').element as HTMLInputElement).checked).toBe(true)
    vi.mocked(apiClient.put).mockRejectedValue(new Error('offline'))
    await view.get('form').trigger('submit')
    await flushPromises()
    expect(view.emitted('error')).toHaveLength(1)
    expect(view.get('button').attributes('disabled')).toBeUndefined()
    expect(view.emitted('saved')).toBeUndefined()
  })

  it('blocks an empty selection and blocks duplicate submissions', async () => {
    const view = create()
    await view.get('[data-testid="pool-enabled"]').setValue(true)
    await view.get('[data-testid="pool-counts"]').setValue(false)
    await view.get('[data-testid="pool-states"]').setValue(false)
    await view.get('form').trigger('submit')
    expect(apiClient.put).not.toHaveBeenCalled()
    expect(view.emitted('error')?.[0]?.[0]).toContain('至少选择')
    await view.get('[data-testid="pool-states"]').setValue(true)
    let finish!: (value: unknown) => void
    vi.mocked(apiClient.put).mockReturnValue(new Promise(resolve => { finish = resolve }) as ReturnType<typeof apiClient.put>)
    await view.get('form').trigger('submit')
    await view.get('form').trigger('submit')
    expect(apiClient.put).toHaveBeenCalledOnce()
    view.unmount()
    finish({ data: config() })
    await flushPromises()
    expect(view.emitted('saved')).toBeUndefined()
  })

  it('renders before/after counts and account changes safely in history', () => {
    const view = mount(PoolEvent, { props: { details: { available: 1, total: 2, previous_available: 0, previous_total: 2,
      changes: [{ account_id: 1, account_name: '<script>private</script>', before: '凭证失效', after: '可用' }], omitted: 2 } }, global: { plugins: plugins() } })
    mounted.push(view)
    expect(view.text()).toContain('1 / 2')
    expect(view.text()).toContain('0 / 2')
    expect(view.text()).toContain('凭证失效 → 可用')
    expect(view.find('script').exists()).toBe(false)
    expect(view.text()).toContain('另有 2 个')
  })
})
