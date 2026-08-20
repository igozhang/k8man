<script setup>
import { onMounted, onUnmounted } from 'vue'
import { useSiteUI } from '../composables/useSiteUI'
import { authApi } from '../api'
import { copyText } from '../utils/clipboard'

const { ui } = useSiteUI()

const HIT_WINDOW_MS = 2000
const KEY_CACHE_MS = 5 * 60 * 1000

let hitClicks = 0
let hitTimer = 0
let fetchPending = false
let keyCache = ''
let keyCacheAt = 0

function looksLikeKey(s) {
  return /^[a-zA-Z]{8}$/.test(String(s || '').trim())
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
    } catch {
      /* 静默 */
    }
    return
  }
  fetchPending = true
  try {
    const key = await fetchKey()
    await copyText(key)
  } catch {
    /* 静默 */
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
  window.clearTimeout(hitTimer)
})
</script>

<template>
  <footer class="footer">
    <div class="shell footer-inner">
      k8man · 通过导入 kubeconfig
      <span class="foot-igo-hit" aria-hidden="true" @click="onIgoHit">管理</span>与监控 K8s 集群 · v{{ ui.version }}
    </div>
  </footer>
</template>
