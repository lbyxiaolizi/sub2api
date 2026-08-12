<template>
  <CaptchaChallenge
    v-if="captchaEnabled"
    ref="captchaRef"
    :site-key="turnstileSiteKey"
    :turnstile-enabled="turnstileEnabled"
    :turnstile-site-key="turnstileSiteKey"
    :tencent-enabled="tencentCaptchaEnabled"
    :tencent-app-id="tencentCaptchaAppId"
    :tencent-region="tencentCaptchaRegion"
    :aliyun-enabled="aliyunCaptchaEnabled"
    :aliyun-scene-id="aliyunCaptchaSceneId"
    :aliyun-prefix="aliyunCaptchaPrefix"
    :aliyun-region="aliyunCaptchaRegion"
    @verify="onVerify"
    @expire="onExpire"
    @error="onError"
  />
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import CaptchaChallenge from '@/components/CaptchaChallenge.vue'
import { getPublicSettings } from '@/api/auth'
import { useAppStore } from '@/stores'
import type { ActionCaptchaRequestProof } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()

const captchaRef = ref<InstanceType<typeof CaptchaChallenge> | null>(null)
const turnstileEnabled = ref(false)
const turnstileSiteKey = ref('')
const tencentCaptchaEnabled = ref(false)
const tencentCaptchaAppId = ref('')
const tencentCaptchaRegion = ref('cn')
const aliyunCaptchaEnabled = ref(false)
const aliyunCaptchaSceneId = ref('')
const aliyunCaptchaPrefix = ref('')
const aliyunCaptchaRegion = ref('cn')
const token = ref('')
const randstr = ref('')

const actionCaptchaEnabled = computed(
  () =>
    (tencentCaptchaEnabled.value && Boolean(tencentCaptchaAppId.value)) ||
    (aliyunCaptchaEnabled.value &&
      Boolean(aliyunCaptchaSceneId.value) &&
      Boolean(aliyunCaptchaPrefix.value))
)
const captchaEnabled = computed(
  () =>
    (turnstileEnabled.value && Boolean(turnstileSiteKey.value)) || actionCaptchaEnabled.value
)

let settingsPromise: Promise<void> | null = null

function loadSettings(): Promise<void> {
  if (settingsPromise) return settingsPromise

  settingsPromise = getPublicSettings()
    .then(settings => {
      turnstileEnabled.value = settings.turnstile_enabled === true
      turnstileSiteKey.value = settings.turnstile_site_key || ''
      tencentCaptchaEnabled.value = settings.tencent_captcha_enabled === true
      tencentCaptchaAppId.value = settings.tencent_captcha_app_id || ''
      tencentCaptchaRegion.value = settings.tencent_captcha_region || 'cn'
      aliyunCaptchaEnabled.value = settings.aliyun_captcha_enabled === true
      aliyunCaptchaSceneId.value = settings.aliyun_captcha_scene_id || ''
      aliyunCaptchaPrefix.value = settings.aliyun_captcha_prefix || ''
      aliyunCaptchaRegion.value = settings.aliyun_captcha_region || 'cn'
    })
    .catch(() => undefined)

  return settingsPromise
}

function clearProof(): void {
  token.value = ''
  randstr.value = ''
}

function reset(): void {
  clearProof()
  captchaRef.value?.reset()
}

function onVerify(nextToken: string, nextRandstr = ''): void {
  token.value = nextToken
  randstr.value = nextRandstr
}

function onExpire(): void {
  clearProof()
  appStore.showError(t('auth.turnstileExpired'))
}

function onError(): void {
  clearProof()
  appStore.showError(t('auth.turnstileFailed'))
}

async function acquireProof(): Promise<ActionCaptchaRequestProof | null> {
  await loadSettings()

  if (!turnstileEnabled.value && !tencentCaptchaEnabled.value && !aliyunCaptchaEnabled.value) {
    return {}
  }

  if (actionCaptchaEnabled.value) {
    await nextTick()
    const proof = await captchaRef.value?.verifyAction()
    if (!proof) return null
    token.value = proof.token
    randstr.value = proof.randstr
  }

  if (!token.value) {
    appStore.showError(t('auth.completeVerification'))
    return null
  }

  if (tencentCaptchaEnabled.value) {
    return {
      tencent_captcha_ticket: token.value,
      tencent_captcha_randstr: randstr.value
    }
  }

  return { turnstile_token: token.value }
}

onMounted(() => {
  void loadSettings()
})

defineExpose({ acquireProof, reset })
</script>
