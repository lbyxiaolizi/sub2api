import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OAuthCompleteRegistrationCaptcha from '../OAuthCompleteRegistrationCaptcha.vue'

const getPublicSettings = vi.fn()
const showError = vi.fn()
const reset = vi.fn()
const verifyAction = vi.fn()

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

vi.mock('@/api/auth', () => ({
  getPublicSettings: (...args: unknown[]) => getPublicSettings(...args)
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError })
}))

describe('OAuthCompleteRegistrationCaptcha', () => {
  beforeEach(() => {
    getPublicSettings.mockReset()
    showError.mockReset()
    reset.mockReset()
    verifyAction.mockReset()
  })

  it('returns no fields when captcha is disabled', async () => {
    getPublicSettings.mockResolvedValue({
      turnstile_enabled: false,
      tencent_captcha_enabled: false,
      aliyun_captcha_enabled: false
    })
    const wrapper = mount(OAuthCompleteRegistrationCaptcha)
    await flushPromises()

    await expect((wrapper.vm as unknown as { acquireProof(): Promise<object> }).acquireProof()).resolves.toEqual({})
  })

  it('acquires Tencent proof and clears it after reset', async () => {
    getPublicSettings.mockResolvedValue({
      turnstile_enabled: false,
      tencent_captcha_enabled: true,
      tencent_captcha_app_id: 'app-id'
    })
    verifyAction.mockResolvedValueOnce({ token: 'ticket-1', randstr: '@rand-1' }).mockResolvedValueOnce(null)
    const CaptchaChallengeStub = defineComponent({
      setup(_, { expose }) {
        expose({ verifyAction, reset })
        return () => h('div')
      }
    })
    const wrapper = mount(OAuthCompleteRegistrationCaptcha, {
      global: { stubs: { CaptchaChallenge: CaptchaChallengeStub } }
    })
    await flushPromises()
    const component = wrapper.vm as unknown as {
      acquireProof(): Promise<object | null>
      reset(): void
    }

    await expect(component.acquireProof()).resolves.toEqual({
      tencent_captcha_ticket: 'ticket-1',
      tencent_captcha_randstr: '@rand-1'
    })
    component.reset()
    await expect(component.acquireProof()).resolves.toBeNull()

    expect(verifyAction).toHaveBeenCalledTimes(2)
    expect(reset).toHaveBeenCalledOnce()
  })
})
