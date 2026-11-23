import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ChannelFields from '../AccountOpsChannelFields.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const webhook = () => ({ id: 'saved', provider: 'feishu' as const, enabled: true, url_configured: true, secret_configured: true })
afterEach(() => vi.unstubAllGlobals())
describe('account notification credentials', () => {
  it('creates robot identifiers on a LAN HTTP origin without crypto.randomUUID', async () => {
    const getRandomValues = globalThis.crypto.getRandomValues.bind(globalThis.crypto)
    vi.stubGlobal('crypto', { getRandomValues })
    const wrapper = mount(ChannelFields, { props: { modelValue: [], encryptionConfigured: true } })
    await wrapper.get('[data-testid="account-ops-add-webhook"]').trigger('click')
    const hooks = wrapper.emitted('update:modelValue')?.[0]?.[0] as any[]
    expect(hooks).toHaveLength(1)
    expect(hooks[0].id).toMatch(/^[a-zA-Z0-9_-]{1,64}$/)
    wrapper.unmount()
  })

  it('retains saved credentials using blank password inputs, and keeps edits out of the shared configuration', async () => {
    const wrapper = mount(ChannelFields, { props: { modelValue: [webhook()], encryptionConfigured: true } })
    const url = wrapper.get('[data-testid="account-ops-webhook-url-saved"]')
    expect((url.element as HTMLInputElement).value).toBe('')
    expect(url.attributes('type')).toBe('password')
    expect(wrapper.find('[data-testid="account-ops-bark-sound"]').exists()).toBe(false)
    expect((wrapper.vm as any).prepare()).toEqual([{ id: 'saved', provider: 'auto', enabled: true }])
    await url.setValue('https://open.feishu.cn/open-apis/bot/v2/hook/test-only')
    await wrapper.get('[data-testid="account-ops-webhook-secret-saved"]').setValue('test-only-secret')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect((wrapper.vm as any).prepare()[0]).toMatchObject({ url: 'https://open.feishu.cn/open-apis/bot/v2/hook/test-only', secret: 'test-only-secret' })
    expect((wrapper.vm as any).hasSensitiveChanges).toBe(true)
    ;(wrapper.vm as any).clearInputs()
    expect((wrapper.vm as any).prepare()[0]).not.toHaveProperty('secret')
    wrapper.unmount()
  })
  it('supports explicit signing-secret removal and saved-only tests', async () => {
    const wrapper = mount(ChannelFields, { props: { modelValue: [webhook()], encryptionConfigured: true } })
    await wrapper.get('[data-testid="account-ops-webhook-test-saved"]').trigger('click')
    expect(wrapper.emitted('test')).toEqual([['saved']])
    await wrapper.get('[data-testid="account-ops-webhook-clear-secret-saved"]').setValue(true)
    expect((wrapper.vm as any).prepare()[0].clear_secret).toBe(true)
    expect(wrapper.get('[data-testid="account-ops-webhook-test-saved"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
  it('automatically adapts known URLs and enables a custom JSON body for other services', async () => {
    const wrapper = mount(ChannelFields, { props: { modelValue: [webhook()], encryptionConfigured: true } })
    const url = wrapper.get('[data-testid="account-ops-webhook-url-saved"]')
    await url.setValue('https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-only')
    expect(wrapper.find('[data-testid="account-ops-webhook-secret-saved"]').exists()).toBe(false)
    await url.setValue('https://hooks.example.com/notify')
    await wrapper.get('[data-testid="account-ops-webhook-template-saved"]').setValue('{"content":"{{message}}"}')
    expect((wrapper.vm as any).prepare()[0]).toMatchObject({ provider: 'auto', message_template: '{"content":"{{message}}"}' })
    expect(wrapper.get('[data-testid="account-ops-webhook-test-saved"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('edits and clears an existing Bark sound without changing other JSON values', async () => {
    const template = '{"title":"{{title}}","body":"{{message}}","sound":"bell","id":1234567890123456789,"extra":{"sound":"nested"}}'
    const hook = { ...webhook(), provider: 'custom' as const, message_template: template }
    const wrapper = mount(ChannelFields, { props: { modelValue: [hook], encryptionConfigured: true } })
    const sound = wrapper.get<HTMLInputElement>('[data-testid="account-ops-bark-sound"]')
    const editor = wrapper.get<HTMLTextAreaElement>('[data-testid="account-ops-webhook-template-saved"]')
    expect(sound.element.value).toBe('bell')
    expect(wrapper.vm.prepare()[0]).not.toHaveProperty('message_template')
    await sound.setValue('alarm')
    expect(wrapper.vm.prepare()[0].message_template).toBe(template.replace('"bell"', '"alarm"'))
    expect(wrapper.get('[data-testid="account-ops-webhook-test-saved"]').attributes('disabled')).toBeDefined()
    await sound.setValue('')
    expect(editor.element.value).toBe(template.replace('"sound":"bell",', ''))
    await editor.setValue(template)
    expect(sound.element.value).toBe('bell')
    expect(wrapper.vm.prepare()[0]).not.toHaveProperty('message_template')
    wrapper.unmount()
  })

  it('starts with the Bark payload for an empty template and protects unfinished JSON', async () => {
    const hook = { ...webhook(), provider: 'custom' as const }
    const wrapper = mount(ChannelFields, { props: { modelValue: [hook], encryptionConfigured: true } })
    const sound = wrapper.get<HTMLInputElement>('[data-testid="account-ops-bark-sound"]')
    await sound.setValue('alarm')
    expect(JSON.parse(wrapper.vm.prepare()[0].message_template!)).toEqual({
      title: '{{title}}', body: '{{message}}', group: 'Sub2API', sound: 'alarm',
    })
    await wrapper.get('[data-testid="account-ops-webhook-template-saved"]').setValue('{"body":')
    expect(sound.element.disabled).toBe(true)
    expect(wrapper.vm.prepare()[0].message_template).toBe('{"body":')
    wrapper.unmount()
  })

})
