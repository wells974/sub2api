
vi.mock('@/api/admin/credentialEncryption', () => ({
  getCredentialEncryption: vi.fn().mockResolvedValue({ configured: true, source: 'server_config' }),
  initializeCredentialEncryption: vi.fn(),
}))
import { getCredentialEncryption, initializeCredentialEncryption } from '@/api/admin/credentialEncryption'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import OpenAITwoFAImport from '../OpenAITwoFAImport.vue'
import { deleteTwoFALogin, getTwoFALogin, startTwoFALogin, startExcelTwoFALogin, getExcelTwoFALogin, deleteExcelTwoFALogin } from '@/api/admin/openaiRelogin'

vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/openaiRelogin', async importOriginal => ({
  ...await importOriginal<typeof import('@/api/admin/openaiRelogin')>(),
  startTwoFALogin: vi.fn(), getTwoFALogin: vi.fn(), deleteTwoFALogin: vi.fn(),
  startExcelTwoFALogin: vi.fn(), getExcelTwoFALogin: vi.fn(), deleteExcelTwoFALogin: vi.fn(),
}))
enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(getCredentialEncryption).mockResolvedValue({ configured: true, source: 'server_config' })
  vi.mocked(startTwoFALogin).mockResolvedValue({ id: 'job-1', status: 'running' })
  vi.mocked(getTwoFALogin).mockResolvedValue({ id: 'job-1', status: 'succeeded', credential: { access_token: 'mock-access' } })
  vi.mocked(deleteTwoFALogin).mockResolvedValue()
})

async function start(wrapper: ReturnType<typeof mount>, input: string) {
  await flushPromises()
  await wrapper.get('textarea').setValue(input)
  await wrapper.get('button').trigger('click')
  await flushPromises()
}

describe('OpenAI initial 2FA import', () => {
  it('imports each account, skips completed rows, and retries creation without logging in again', async () => {
    const importCredential = vi.fn().mockResolvedValueOnce('created').mockRejectedValueOnce(new Error('do not display credential'))
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential } })
    await start(wrapper, 'a@example.com----p1----s1\nb@example.com----p2----s2')
    expect(importCredential).toHaveBeenCalledTimes(2)
    expect(startTwoFALogin).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('openaiRelogin.twoFA.states.created')
    expect(wrapper.text()).toContain('openaiRelogin.twoFA.states.importFailed')
    expect(wrapper.text()).not.toContain('do not display credential')
    expect(wrapper.find('textarea').exists()).toBe(false)
    importCredential.mockResolvedValueOnce('created')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(importCredential).toHaveBeenCalledTimes(3)
    expect(startTwoFALogin).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).not.toContain('openaiRelogin.twoFA.retry')
    expect(importCredential.mock.calls[1]?.[2]).toEqual({ email: 'b@example.com', password: 'p2', mfa_secret: 's2' })
    expect(importCredential.mock.calls[2]?.[2]).toEqual({ email: 'b@example.com', password: 'p2', mfa_secret: 's2' })
    expect(wrapper.emitted('busy')).toEqual([[true], [false], [true], [false]])
  })

  it('never imports when login fails, and rejects malformed batch lines before sending anything', async () => {
    const importCredential = vi.fn()
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential } })
    await start(wrapper, 'a@example.com----p----s\ninvalid')
    expect(startTwoFALogin).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('invalid')
    vi.mocked(getTwoFALogin).mockResolvedValue({ id: 'job-1', status: 'failed' })
    await start(wrapper, 'a@example.com----p----s')
    expect(importCredential).not.toHaveBeenCalled()
    expect(deleteTwoFALogin).toHaveBeenCalledWith('job-1')
  })

  it('resumes a job after a polling failure instead of starting another login', async () => {
    const importCredential = vi.fn().mockResolvedValue('created')
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential } })
    vi.mocked(getTwoFALogin).mockRejectedValueOnce(new Error('network failure'))
    await start(wrapper, 'a@example.com----p----s')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(startTwoFALogin).toHaveBeenCalledTimes(1)
    expect(importCredential).toHaveBeenCalledTimes(1)
  })

  it('stops all active logins without importing or starting queued rows', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(getTwoFALogin).mockResolvedValue({ id: 'job-1', status: 'running' })
      const importCredential = vi.fn()
      const wrapper = mount(OpenAITwoFAImport, { props: { importCredential } })
      await start(wrapper, 'a@example.com----p----s\nb@example.com----p----s\nc@example.com----p----s\nd@example.com----p----s')
      await wrapper.get('button').trigger('click')
      await vi.advanceTimersByTimeAsync(1500)
      await flushPromises()
      expect(deleteTwoFALogin).toHaveBeenCalledWith('job-1')
      expect(importCredential).not.toHaveBeenCalled()
      expect(startTwoFALogin).toHaveBeenCalledTimes(3)
    } finally {
      vi.useRealTimers()
    }
  })
})


it('blocks 2FA login and account creation until encryption initialization succeeds', async () => {
  vi.mocked(getCredentialEncryption).mockResolvedValue({ configured: false, source: 'unconfigured' })
  vi.mocked(initializeCredentialEncryption).mockResolvedValue({ configured: true, source: 'local_file' })
  const importCredential = vi.fn().mockResolvedValue('created')
  const wrapper = mount(OpenAITwoFAImport, { props: { importCredential } })
  await flushPromises()
  await wrapper.get('textarea').setValue('a@example.com----password----secret')
  expect(wrapper.get('[data-testid="two-fa-start"]').attributes('disabled')).toBeDefined()
  await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
  expect(startTwoFALogin).not.toHaveBeenCalled()
  expect(importCredential).not.toHaveBeenCalled()
  await wrapper.get('[data-testid="initialize-credential-encryption"]').trigger('click')
  await flushPromises()
  expect(wrapper.get('[data-testid="two-fa-start"]').attributes('disabled')).toBeUndefined()
  await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
  await flushPromises()
  expect(importCredential).toHaveBeenCalledTimes(1)
})


describe('BPS initial 2FA import', () => {
  it('uses the Excel queue and selected proxy, then retries import without repeating authorization', async () => {
    const credential = { access_token: 'excel-at', client_id: 'app_fnr0pYvVwwFDocDumLG3H2Bp' }
    vi.mocked(startExcelTwoFALogin).mockResolvedValue({ id: 'excel-job', status: 'running' })
    vi.mocked(getExcelTwoFALogin).mockResolvedValue({ id: 'excel-job', status: 'succeeded', credential })
    vi.mocked(deleteExcelTwoFALogin).mockResolvedValue()
    const importCredential = vi.fn().mockRejectedValueOnce(new Error('PRIVATE_ERROR')).mockResolvedValueOnce('created')
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential, excelOauth: true, proxyId: 12 } })
    await start(wrapper, 'bps@example.com----password----totp')
    expect(startExcelTwoFALogin).toHaveBeenCalledWith({ email: 'bps@example.com', password: 'password', mfa_secret: 'totp' }, 12)
    expect(deleteExcelTwoFALogin).toHaveBeenCalledWith('excel-job')
    expect(startTwoFALogin).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('PRIVATE_ERROR')
    await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
    await flushPromises()
    expect(startExcelTwoFALogin).toHaveBeenCalledTimes(1)
    expect(importCredential).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('openaiRelogin.twoFA.states.created')
  })

  it('retires an accepted Excel job when stop races with its POST response', async () => {
    let accept!: (job: { id: string; status: 'running' }) => void
    vi.mocked(startExcelTwoFALogin).mockImplementation(() => new Promise(resolve => { accept = resolve }))
    vi.mocked(deleteExcelTwoFALogin).mockResolvedValue()
    const importCredential = vi.fn()
    const wrapper = mount(OpenAITwoFAImport, { props: { importCredential, excelOauth: true } })
    await start(wrapper, 'bps@example.com----password----totp')
    await wrapper.get('[data-testid="two-fa-stop"]').trigger('click')
    // A parent reset must not redirect cleanup into the Codex job namespace.
    await wrapper.setProps({ excelOauth: false })
    accept({ id: 'late-excel-job', status: 'running' })
    await flushPromises()
    expect(deleteExcelTwoFALogin).toHaveBeenCalledWith('late-excel-job')
    expect(importCredential).not.toHaveBeenCalled()
    expect(getExcelTwoFALogin).not.toHaveBeenCalled()
  })
})

it.each([
  ['EXCEL_LOGIN_WORKER_PREPARING', undefined, 'openaiRelogin.twoFA.failures.workerPreparing'],
  ['EXCEL_LOGIN_WORKER_UNAVAILABLE', undefined, 'openaiRelogin.twoFA.failures.excelWorkerUnavailable'],
  ['OPENAI_REAUTH_WORKER_UNAVAILABLE', 'release_required', 'tokenGuardV2.runtimeReasons.release_required'],
  ['OPENAI_REAUTH_WORKER_UNAVAILABLE', 'runtime_install_failed', 'tokenGuardV2.runtimeReasons.runtime_install_failed'],
  ['OPENAI_REAUTH_WORKER_UNAVAILABLE', 'runtime_source_invalid', 'tokenGuardV2.runtimeReasons.runtime_source_invalid'],
  ['OPENAI_REAUTH_WORKER_UNAVAILABLE', 'PRIVATE_REASON', 'openaiRelogin.twoFA.failures.workerUnavailable'],
  ['EXCEL_LOGIN_PROXY_UNAVAILABLE', undefined, 'openaiRelogin.twoFA.failures.proxyUnavailable'],
  ['PRIVATE_REASON', undefined, 'openaiRelogin.twoFA.states.failed'],
  ['__proto__', undefined, 'openaiRelogin.twoFA.states.failed'],
])('shows a safe actionable failure for %s / %s and clears it after retry', async (reason, workerReason, key) => {
  vi.mocked(startExcelTwoFALogin).mockRejectedValueOnce({
    status: 503, reason, message: 'PRIVATE_PASSWORD PRIVATE_TOTP PRIVATE_TOKEN',
    metadata: { worker_reason: workerReason },
  }).mockResolvedValue({ id: 'retry-job', status: 'running' })
  vi.mocked(getExcelTwoFALogin).mockResolvedValue({ id: 'retry-job', status: 'succeeded', credential: { access_token: 'mock-access' } })
  vi.mocked(deleteExcelTwoFALogin).mockResolvedValue()
  const importCredential = vi.fn().mockResolvedValue('created')
  const wrapper = mount(OpenAITwoFAImport, { props: { importCredential, excelOauth: true } })
  await start(wrapper, 'a@example.com----password----totp')
  expect(wrapper.text()).toContain(key)
  expect(wrapper.text()).not.toContain('PRIVATE_')
  expect(importCredential).not.toHaveBeenCalled()
  expect(getExcelTwoFALogin).not.toHaveBeenCalled()
  await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
  await flushPromises()
  expect(wrapper.text()).not.toContain(key)
  expect(wrapper.text()).toContain('openaiRelogin.twoFA.states.created')
  expect(importCredential).toHaveBeenCalledTimes(1)
})

it.each([
  ['transport_error', 'protocol_connecting', 'openaiRelogin.twoFA.excelFailures.transport_error'],
  ['password_rejected', 'web_login', 'openaiRelogin.twoFA.excelFailures.password_rejected'],
  ['totp_rejected', 'mfa_challenge', 'openaiRelogin.twoFA.excelFailures.totp_rejected'],
  ['runtime_error', 'starting', 'openaiRelogin.twoFA.excelFailures.runtime_error'],
  ['email_verification', 'web_login', 'openaiRelogin.twoFA.excelFailures.email_verification'],
  ['phone_verification', 'web_login', 'openaiRelogin.twoFA.excelFailures.phone_verification'],
  ['interaction_required', 'web_login', 'openaiRelogin.twoFA.excelFailures.interaction_required'],
  ['unexpected_login_step', 'web_login', 'openaiRelogin.twoFA.excelFailures.unexpected_login_step'],
  ['PRIVATE_TOKEN', 'PRIVATE_PASSWORD', 'openaiRelogin.twoFA.states.failed'],
  [undefined, undefined, 'openaiRelogin.twoFA.states.failed'],
])('shows failed BPS job diagnostics safely (%s)', async (errorCode, errorStage, key) => {
  vi.mocked(startExcelTwoFALogin).mockResolvedValue({ id: 'failed-job', status: 'running' })
  vi.mocked(getExcelTwoFALogin).mockResolvedValue({ id: 'failed-job', status: 'failed', error_code: errorCode, error_stage: errorStage })
  vi.mocked(deleteExcelTwoFALogin).mockResolvedValue()
  const importCredential = vi.fn().mockResolvedValue('created')
  const wrapper = mount(OpenAITwoFAImport, { props: { importCredential, excelOauth: true } })
  await start(wrapper, 'a@example.com----password----totp')
  expect(wrapper.text()).toContain(key)
  expect(wrapper.text()).not.toContain('PRIVATE_')
  expect(importCredential).not.toHaveBeenCalled()
  expect(deleteExcelTwoFALogin).toHaveBeenCalledWith('failed-job')
  vi.mocked(getExcelTwoFALogin).mockResolvedValue({ id: 'failed-job', status: 'succeeded', credential: { access_token: 'mock' } })
  await wrapper.get('[data-testid="two-fa-start"]').trigger('click')
  await flushPromises()
  expect(wrapper.text()).not.toContain(key)
  expect(wrapper.text()).not.toContain('openaiRelogin.twoFA.failureStage')
  expect(wrapper.text()).toContain('openaiRelogin.twoFA.states.created')
})
