import { computed, reactive } from 'vue'
import { authApi } from '../api/auth'
import { onWriteAuthLost, setWriteToken } from '../api/http'

const state = reactive({
  token: '',
  expiresAt: 0,
  busy: false,
  now: Math.floor(Date.now() / 1000)
})

let expiryTimer = 0
let sessionTimer = 0

export const canWrite = computed(() => {
  if (!state.token || !state.expiresAt) return false
  return state.now < state.expiresAt
})

export function remainingSec() {
  if (!state.expiresAt) return 0
  return Math.max(0, state.expiresAt - state.now)
}

export function fmtBeijing(ts) {
  const n = Number(ts)
  if (!n) return '—'
  try {
    return new Date(n * 1000).toLocaleString('zh-CN', {
      timeZone: 'Asia/Shanghai',
      hour12: false
    })
  } catch {
    return new Date(n * 1000).toISOString()
  }
}

function stopWatch() {
  window.clearInterval(expiryTimer)
  window.clearInterval(sessionTimer)
  expiryTimer = 0
  sessionTimer = 0
}

function applyToken(token, expiresInSec) {
  const sec = typeof expiresInSec === 'number' && expiresInSec > 0 ? expiresInSec : 0
  state.token = token || ''
  state.expiresAt = sec ? Math.floor(Date.now() / 1000) + sec : 0
  setWriteToken(state.token)
}

export function logout() {
  applyToken('', 0)
  stopWatch()
}

async function checkSession() {
  if (!state.token) return
  try {
    const data = await authApi.session(state.token)
    if (typeof data?.expiresInSec === 'number') {
      state.expiresAt = Math.floor(Date.now() / 1000) + Math.max(0, data.expiresInSec)
    }
  } catch (e) {
    logout()
  }
}

function tickExpiry() {
  state.now = Math.floor(Date.now() / 1000)
  if (!state.token) return
  if (remainingSec() <= 0) logout()
}

function startWatch() {
  stopWatch()
  tickExpiry()
  checkSession()
  expiryTimer = window.setInterval(tickExpiry, 1000)
  sessionTimer = window.setInterval(checkSession, 15000)
}

export async function login(key) {
  const k = String(key || '').trim()
  if (!k) throw new Error('请输入密钥')
  state.busy = true
  try {
    const data = await authApi.login(k)
    if (!data?.token) throw new Error('验证失败')
    applyToken(data.token, data.expiresInSec)
    startWatch()
    return data
  } finally {
    state.busy = false
  }
}

onWriteAuthLost(() => {
  logout()
})

export function useOpsAuth() {
  return {
    state,
    canWrite,
    remainingSec,
    fmtBeijing,
    login,
    logout
  }
}
