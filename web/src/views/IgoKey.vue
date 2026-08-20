<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { authApi } from '../api'
import { copyText } from '../utils/clipboard'
import { errMessage } from '../utils/safe'
import { fmtBeijing } from '../stores/opsAuth'

const key = ref('')
const expiresAt = ref(0)
const error = ref('')
const status = ref('')
const statusOk = ref(null)
const busy = ref(false)
const nowTick = ref(0)
let timer = 0

const remainMin = computed(() => {
  nowTick.value
  if (!expiresAt.value) return 0
  const left = expiresAt.value - Math.floor(Date.now() / 1000)
  return left > 0 ? Math.max(1, Math.ceil(left / 60)) : 0
})

const expText = computed(() => fmtBeijing(expiresAt.value))

function apply(data) {
  key.value = data?.key || ''
  const sec = typeof data?.expiresInSec === 'number' ? data.expiresInSec : 0
  expiresAt.value = sec > 0 ? Math.floor(Date.now() / 1000) + sec : 0
}

async function load({ renew = false } = {}) {
  busy.value = true
  error.value = ''
  try {
    apply(await authApi.igokey({ renew }))
  } catch (e) {
    error.value = errMessage(e)
    key.value = ''
  } finally {
    busy.value = false
  }
}

async function copyKey({ fromBtn = false } = {}) {
  if (!key.value) return
  try {
    await copyText(key.value)
    status.value = fromBtn ? '已复制到剪贴板' : '验证密钥已复制到剪贴板'
    statusOk.value = true
  } catch {
    status.value = '无法自动复制，请点击「复制」或手动选中密钥'
    statusOk.value = false
  }
}

async function renew() {
  status.value = ''
  await load({ renew: true })
  if (key.value) copyKey({ fromBtn: false })
}

onMounted(async () => {
  await load()
  if (key.value) copyKey({ fromBtn: false })
  timer = window.setInterval(() => {
    nowTick.value = Date.now()
  }, 1000)
})

onUnmounted(() => {
  window.clearInterval(timer)
})
</script>

<template>
  <div>
    <div class="toolbar">
      <div>
        <h1>验证密钥</h1>
        <p class="page-desc">有效期约 1 小时。剩余超过 15 分钟时刷新本页沿用当前密钥；不足 15 分钟或点「重新生成」会签发新密钥并作废旧密钥。</p>
      </div>
    </div>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-else-if="busy && !key" class="muted">正在签发密钥…</p>
    <div v-else class="panel igokey-panel">
      <p class="igokey-value" aria-label="验证密钥">{{ key || '—' }}</p>
      <div class="toolbar toolbar-start igokey-actions">
        <button type="button" class="btn" :disabled="!key" @click="copyKey({ fromBtn: true })">复制</button>
        <button type="button" class="btn" :disabled="busy" @click="renew">重新生成</button>
      </div>
      <p class="muted igokey-hint">
        至 <time>{{ expText }}</time>（北京时间，剩余 {{ remainMin }} 分钟）
      </p>
      <p class="igokey-status" :class="{ 'is-ok': statusOk === true, 'is-err': statusOk === false }" aria-live="polite">{{ status }}</p>
    </div>
  </div>
</template>
