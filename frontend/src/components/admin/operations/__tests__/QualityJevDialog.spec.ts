import { flushPromises, mount } from '@vue/test-utils'
import { expect, it, vi } from 'vitest'
import Dialog from '../QualityJevDialog.vue'
import { getAll as getProxies } from '@/api/admin/proxies'
import { getJevSettings, saveJevSettings } from '@/api/admin/qualityJev'

vi.mock('@/api/admin/qualityJev', () => ({ getJevSettings: vi.fn(), saveJevSettings: vi.fn() }))
vi.mock('@/api/admin/proxies', () => ({ getAll: vi.fn().mockResolvedValue([]) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

it('enables grading with an existing key and adds a second key at its own endpoint', async () => {
  vi.mocked(getJevSettings).mockResolvedValue({
    revision: 'revision', enabled: false, base_url: 'https://api.typesafe.ai',
    proxy_id: null, timeout_ms: 3000, retry_count: 1,
    keys: [{ id: 'existing', masked: '********1234', base_url: '' }],
  })
  const view = mount(Dialog, { global: { stubs: {
    BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' },
  } } })
  await flushPromises()
  expect(view.find('input[type=password]').exists()).toBe(false)
  await view.get('input[type=checkbox]').setValue(true)
  const add = view.findAll('button').find(b => b.text() === 'qualityOps.jevSettings.addKey')!
  await add.trigger('click')
  await view.get('input[type=password]').setValue('synthetic-second')
  await view.findAll('input[type=url]')[2]!.setValue('https://second.example')
  await view.get('form').trigger('submit')
  await flushPromises()
  expect(saveJevSettings).toHaveBeenCalledWith(expect.objectContaining({
    enabled: true, revision: 'revision',
    keys: [{ id: 'existing', masked: '********1234', base_url: '' }, { key: 'synthetic-second', base_url: 'https://second.example' }],
  }))
  expect(view.emitted('saved')).toHaveLength(1)
  view.unmount()
})

it('preserves the saved proxy and allows editing the judge if proxy discovery fails', async () => {
  vi.mocked(getProxies).mockRejectedValueOnce(new Error('proxy catalog unavailable'))
  vi.mocked(getJevSettings).mockResolvedValue({
    revision: 'proxy-revision', enabled: true, base_url: 'https://api.typesafe.ai',
    proxy_id: 9, timeout_ms: 3000, retry_count: 1,
    keys: [{ id: 'existing', masked: '********1234', base_url: '' }],
  })
  const view = mount(Dialog, { global: { stubs: {
    BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' },
  } } })
  await flushPromises()
  expect(view.get('select').element.disabled).toBe(true)
  expect(view.get('select').element.value).toBe('9')
  await view.get('input[type=checkbox]').setValue(false)
  await view.get('form').trigger('submit')
  await flushPromises()
  expect(saveJevSettings).toHaveBeenLastCalledWith(expect.objectContaining({
    enabled: false, proxy_id: 9, keys: [{ id: 'existing', masked: '********1234', base_url: '' }],
  }))
  expect(view.emitted('saved')).toHaveLength(1)
  view.unmount()
})
