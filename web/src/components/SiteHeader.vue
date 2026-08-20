<script setup>
import { nextTick, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useSiteUI } from '../composables/useSiteUI'
import { canWrite, login, useOpsAuth } from '../stores/opsAuth'

const { ui, iconSrc, bannerStyle } = useSiteUI()
const { state } = useOpsAuth()

const open = ref(false)
const key = ref('')
const keyInput = ref(null)

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
          <RouterLink to="/" class="brand-home brand-home-icon">
            <img class="brand-icon" :src="iconSrc" width="36" height="36" alt="" />
          </RouterLink>
          <div class="brand-copy">
            <RouterLink to="/" class="brand brand-home">{{ ui.title }}</RouterLink>
            <p v-if="ui.tagline" class="site-tagline">{{ ui.tagline }}</p>
          </div>
        </div>
        <nav class="nav">
          <RouterLink to="/">概览</RouterLink>
          <RouterLink to="/clusters">KubeConfig</RouterLink>
          <RouterLink to="/workloads">工作负载</RouterLink>
          <RouterLink to="/monitor">监控</RouterLink>
          <RouterLink to="/diagnose">诊断</RouterLink>
          <RouterLink to="/docs">运维文档</RouterLink>
          <span class="nav-perm" aria-hidden="true" @dblclick.prevent="onPermDblclick">{{ canWrite ? '可操作' : '只读' }}</span>
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
