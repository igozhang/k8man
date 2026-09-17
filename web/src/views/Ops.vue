<script setup>
import { computed, ref } from 'vue'
import { useOpsAuth } from '../stores/opsAuth'

const { state, canWrite, remainingSec, fmtBeijing, login, logout } = useOpsAuth()
const key = ref('')
const msg = ref('')
const msgOk = ref(null)

const expiryText = computed(() => {
  if (!canWrite.value || !state.expiresAt) return ''
  const mins = Math.max(0, Math.ceil(remainingSec() / 60))
  return `密钥有效期至 ${fmtBeijing(state.expiresAt)}（北京时间，剩余 ${mins} 分钟）`
})

const expiryWarn = computed(() => canWrite.value && remainingSec() < 5 * 60)

async function submit() {
  msg.value = ''
  msgOk.value = null
  try {
    await login(key.value)
    msg.value = '验证通过'
    msgOk.value = true
    key.value = ''
  } catch (e) {
    msg.value = e?.message || '验证失败'
    msgOk.value = false
  }
}

function onLogout() {
  logout()
  msg.value = '已退出可操作权限，当前为只读'
  msgOk.value = null
}
</script>

<template>
  <div>
    <div class="toolbar">
      <div>
        <h1>IGO密钥认证</h1>
        <p class="page-desc">默认只读。输入有效密钥后可进行导入、重启、扩缩容等写操作；保持可操作直至主动退出或密钥过期（刷新/跳转不丢失）。也可在页眉双击「只读 / 可操作」切换。</p>
      </div>
    </div>

    <div v-if="!canWrite" class="panel ops-panel">
      <div class="form-row ops-key-row">
        <label for="igoKey">验证密钥（key）</label>
        <input
          id="igoKey"
          v-model="key"
          type="text"
          class="ops-key-input"
          autocomplete="off"
          spellcheck="false"
          maxlength="8"
          placeholder="Input your password ："
          @keydown.enter.prevent="submit"
        />
      </div>
      <div class="toolbar toolbar-start">
        <button type="button" class="btn btn-primary" :disabled="state.busy" @click="submit">
          {{ state.busy ? '验证中…' : '验证' }}
        </button>
      </div>
      <p class="ops-msg" :class="{ 'is-ok': msgOk === true, 'is-err': msgOk === false }" aria-live="polite">{{ msg }}</p>
    </div>

    <div v-else class="panel ops-panel">
      <p class="notice ops-ok">已验证，当前可进行写操作。</p>
      <p class="muted ops-expiry" :class="{ 'is-warn': expiryWarn }">{{ expiryText }}</p>
      <div class="toolbar toolbar-start">
        <button type="button" class="btn" @click="onLogout">退出可操作</button>
      </div>
      <p v-if="msg" class="ops-msg" :class="{ 'is-ok': msgOk === true }">{{ msg }}</p>
    </div>
  </div>
</template>
