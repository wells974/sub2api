import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, expect, it, vi } from 'vitest'
import GroupFallbackPanel from '../GroupFallbackPanel.vue'
import { getGroupFallback, getGroupFallbackOptions, saveGroupFallback } from '@/api/admin/groupFallback'

vi.mock('@/api/admin/groupFallback', () => ({
  getGroupFallback: vi.fn(), getGroupFallbackOptions: vi.fn(), saveGroupFallback: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 1, role: 'admin' }, isAdmin: true }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(getGroupFallback).mockResolvedValue({ config: { revision: 'old', enabled: false, rules: [] }, status: {} })
  vi.mocked(getGroupFallbackOptions).mockResolvedValue([
    { id: 11, name: 'Group A', accounts: [{ id: 52, name: 'Backup A', schedulable: false }] },
    { id: 12, name: 'Group B', accounts: [{ id: 53, name: 'Backup B', schedulable: false }] },
  ])
  vi.mocked(saveGroupFallback).mockImplementation(async c => ({ ...c, revision: 'new' }))
})
it('saves each group with only its selected backups and thresholds', async () => {
  const w = mount(GroupFallbackPanel)
  await flushPromises()
  await w.get('input[role="switch"]').setValue(true)
  const select = w.get('select')
  const add = w.get('button[type="button"]')
  await select.setValue('11')
  await add.trigger('click')
  await select.setValue('12')
  await add.trigger('click')
  const rules = w.findAll('form section')
  expect(rules).toHaveLength(2)
  for (const [i, section] of rules.entries()) {
    expect(section.text()).toContain(i === 0 ? 'Backup A' : 'Backup B')
    expect(section.text()).not.toContain(i === 0 ? 'Backup B' : 'Backup A')
    await section.get('input[type="checkbox"]:not([role])').setValue(true)
    await section.get('input[type="number"]').setValue(90+i)
  }
  await w.get('form').trigger('submit')
  await flushPromises()
  expect(w.find('[role="alert"]').exists()).toBe(false)
  expect(saveGroupFallback).toHaveBeenCalledWith(expect.objectContaining({
    enabled: true, revision: 'old', rules: [
      { group_id: 11, policy: expect.objectContaining({ account_ids: [52], threshold: 90 }) },
      { group_id: 12, policy: expect.objectContaining({ account_ids: [53], threshold: 91 }) },
    ],
  }))
  w.unmount()
})
