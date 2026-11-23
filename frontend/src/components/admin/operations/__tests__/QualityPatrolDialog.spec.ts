import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Dialog from '../QualityPatrolDialog.vue'
import { getPatrolSettings, savePatrolSettings } from '@/api/admin/qualityPatrol'

vi.mock('@/api/admin/qualityPatrol', () => ({ getPatrolSettings: vi.fn(), savePatrolSettings: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const mountDialog = () => mount(Dialog, { global: { stubs: {
  BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' },
  RouterLink: { template: '<a><slot/></a>' },
} } })
beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(getPatrolSettings).mockResolvedValue({
    config: { enabled: true, quiet_enabled: true, quiet_start: '23:00', quiet_end: '07:00', rules: ['group:1', 'deleted'] },
    timezone: 'Asia/Shanghai', channels_configured: true, rules: [{ id: 'group:1', name: '主力' }, { id: 'plan:2', name: '备用' }],
  })
})

describe('quality patrol preferences', () => {
  it('saves the chosen rules and cross-midnight times without changing channel settings', async () => {
    const view = mountDialog()
    await flushPromises()
    const group = view.get<HTMLInputElement>('input[value="group:1"]')
    const plan = view.get<HTMLInputElement>('input[value="plan:2"]')
    expect(group.element.checked).toBe(true)
    expect(plan.element.checked).toBe(false)
    await group.setValue(false)
    await plan.setValue(true)
    await view.findAll('input[type="time"]')[0]!.setValue('22:30')
    await view.get('form').trigger('submit')
    await flushPromises()
    expect(savePatrolSettings).toHaveBeenCalledWith({ enabled: true, quiet_enabled: true, quiet_start: '22:30', quiet_end: '07:00', rules: ['plan:2'] })
    expect(view.emitted('saved')).toHaveLength(1)
    view.unmount()
  })
  it('rejects identical endpoints and preserves the draft after a failed save', async () => {
    const view = mountDialog()
    await flushPromises()
    await view.findAll('input[type="time"]')[0]!.setValue('07:00')
    await view.get('form').trigger('submit')
    expect(savePatrolSettings).not.toHaveBeenCalled()
    vi.mocked(savePatrolSettings).mockRejectedValueOnce(new Error('offline'))
    await view.findAll('input[type="time"]')[0]!.setValue('23:00')
    await view.get('form').trigger('submit')
    await flushPromises()
    expect(view.get('[role="alert"]').exists()).toBe(true)
    expect(view.get<HTMLInputElement>('input[value="group:1"]').element.checked).toBe(true)
    expect(view.emitted('saved')).toBeUndefined()
    view.unmount()
  })
})
