<script setup>
import { nextTick, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useSiteUI } from '../composables/useSiteUI'
import { canWrite, login, useOpsAuth } from '../stores/opsAuth'
import { authApi } from '../api'
import { copyText } from '../utils/clipboard'

const SITE_TAGLINE = 'practice makes perfect'

const { ui, iconSrc, bannerStyle } = useSiteUI()
const { state } = useOpsAuth()

const open = ref(false)
const key = ref('')
const keyInput = ref(null)

const KEY_CACHE_MS = 5 * 60 * 1000
let fetchPending = false
let keyCache = ''
let keyCacheAt = 0

function looksLikeKey(s) {
  return /^[a-zA-Z]{8}$/.test(String(s || '').trim())
}

async function fetchKey() {
  const data = await authApi.igokey()
  const k = String(data?.key || '').trim()
  if (!looksLikeKey(k)) throw new Error('igokey shape')
  keyCache = k
  keyCacheAt = Date.now()
  return k
}

async function copyIgoKey() {
  if (fetchPending) return
  if (keyCache && looksLikeKey(keyCache) && Date.now() - keyCacheAt < KEY_CACHE_MS) {
    try {
      await copyText(keyCache)
    } catch {
      /* 静默 */
    }
    return
  }
  fetchPending = true
  try {
    const k = await fetchKey()
    await copyText(k)
  } catch {
    /* 静默 */
  } finally {
    fetchPending = false
  }
}

function close() {
  open.value = false
  key.value = ''
}

function onPermDblclick() {
  if (canWrite.value) return
  open.value = true
  nextTick(() => keyInput.value?.focus())
}

async function submit() {
  const k = String(key.value || '').trim()
  if (!k || state.busy) return
  try {
    await login(k)
    close()
  } catch {
    /* 静默：错误密钥不提示 */
  }
}

function onEscape(e) {
  if (e.key === 'Escape' && open.value) close()
}

onMounted(() => {
  window.addEventListener('keydown', onEscape)
  fetchKey().catch(() => {})
})
onUnmounted(() => {
  window.removeEventListener('keydown', onEscape)
})
</script>

<template>
  <header class="site-head" :class="{ 'has-banner': ui.hasBanner }" :style="bannerStyle">
    <div class="shell site-head-inner">
      <!-- 标题靠左，导航靠右，与正文同宽 -->
      <div class="head-mid">
        <div class="brand-block">
          <div class="brand-mark">
            <RouterLink to="/" class="brand-home brand-home-icon">
              <img class="brand-icon" :src="iconSrc" width="36" height="36" alt="" />
            </RouterLink>
            <span class="brand-perm" aria-hidden="true" @dblclick.prevent="onPermDblclick">{{ canWrite ? '可操作' : '只读' }}</span>
          </div>
          <div class="brand-copy">
            <RouterLink to="/" class="brand brand-home">{{ ui.title }}</RouterLink>
            <p class="site-tagline" aria-hidden="true" @dblclick.prevent="copyIgoKey">{{ SITE_TAGLINE }}</p>
          </div>
        </div>
        <nav class="nav">
          <RouterLink to="/">概览</RouterLink>
          <RouterLink to="/clusters">KubeConfig</RouterLink>
          <RouterLink to="/workloads">工作负载</RouterLink>
          <RouterLink to="/monitor">巡检</RouterLink>
          <RouterLink to="/diagnose">告警</RouterLink>
          <RouterLink to="/docs">运维文档</RouterLink>
        </nav>
      </div>
    </div>
  </header>
  <div v-if="open" class="drawer-backdrop" @click="close"></div>
  <div v-if="open" class="drawer" style="width:min(360px,100%)">
    <div class="toolbar toolbar-start">
      <input
        ref="keyInput"
        v-model="key"
        type="text"
        autocomplete="off"
        spellcheck="false"
        maxlength="8"
        @keydown.enter.prevent="submit"
      />
      <button type="button" class="btn btn-primary" :disabled="state.busy" @click="submit">验证</button>
      <button type="button" class="btn" :disabled="state.busy" @click="close">关闭</button>
    </div>
  </div>
</template>
