<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { useSiteUI } from '../composables/useSiteUI'
import { authApi } from '../api'
import { copyText } from '../utils/clipboard'

const { ui } = useSiteUI()

const HIT_WINDOW_MS = 2000
const KEY_CACHE_MS = 5 * 60 * 1000

const toast = ref({ text: '', err: false, show: false })
let toastTimer = 0
let hitClicks = 0
let hitTimer = 0
let fetchPending = false
let keyCache = ''
let keyCacheAt = 0

function looksLikeKey(s) {
  return /^[a-zA-Z]{8}$/.test(String(s || '').trim())
}

function showToast(text, isErr) {
  window.clearTimeout(toastTimer)
  toast.value = { text, err: !!isErr, show: true }
  toastTimer = window.setTimeout(() => {
    toast.value = { text: '', err: false, show: false }
  }, isErr ? 5200 : 3800)
}

async function fetchKey() {
  const data = await authApi.igokey()
  const key = String(data?.key || '').trim()
  if (!looksLikeKey(key)) throw new Error('igokey shape')
  keyCache = key
  keyCacheAt = Date.now()
  return key
}

async function copyIgoKey() {
  if (fetchPending) return
  const cached = keyCache
  if (cached && looksLikeKey(cached) && Date.now() - keyCacheAt < KEY_CACHE_MS) {
    try {
      await copyText(cached)
      showToast('验证密钥已复制到剪贴板', false)
    } catch {
      showToast('无法自动复制，请打开 /igokey 手动复制', true)
    }
    return
  }
  fetchPending = true
  try {
    const key = await fetchKey()
    await copyText(key)
    showToast('验证密钥已复制到剪贴板', false)
  } catch {
    showToast('无法自动复制，请打开 /igokey 手动复制', true)
  } finally {
    fetchPending = false
  }
}

function onIgoHit(ev) {
  ev.preventDefault()
  hitClicks += 1
  window.clearTimeout(hitTimer)
  if (hitClicks >= 2) {
    hitClicks = 0
    copyIgoKey()
    return
  }
  hitTimer = window.setTimeout(() => {
    hitClicks = 0
  }, HIT_WINDOW_MS)
}

onMounted(() => {
  fetchKey().catch(() => {})
})

onUnmounted(() => {
  window.clearTimeout(toastTimer)
  window.clearTimeout(hitTimer)
})
</script>

<template>
  <footer class="footer">
    <div class="shell footer-inner">
      k8man · 通过导入 kubeconfig
      <span class="foot-igo-hit" aria-hidden="true" @click="onIgoHit">管理</span>与监控 K8s 集群 · v{{ ui.version }}
    </div>
    <div v-if="toast.show" class="toast-wrap" aria-live="polite">
      <p class="toast" :class="toast.err ? 'toast-err' : 'toast-ok'">{{ toast.text }}</p>
    </div>
  </footer>
</template>
