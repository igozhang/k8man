import { computed, reactive } from 'vue'
import { authApi } from '../api/auth'
import { onWriteAuthLost, setWriteToken } from '../api/http'

const STORAGE_KEY = 'k8man.opsWriteAuth'

const state = reactive({
  token: '',
  expiresAt: 0,
  busy: false,
  now: Math.floor(Date.now() / 1000)
})

let expiryTimer = 0
let sessionTimer = 0
/** 递增以作废进行中的 checkSession，避免旧请求覆盖新登录或误 logout */
let authEpoch = 0

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

function persist() {
  try {
    if (!state.token || !state.expiresAt) {
      sessionStorage.removeItem(STORAGE_KEY)
      return
    }
    sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ token: state.token, expiresAt: state.expiresAt })
    )
  } catch {
    /* ignore quota / private mode */
  }
}

function clearAuth() {
  authEpoch += 1
  state.token = ''
  state.expiresAt = 0
  state.now = Math.floor(Date.now() / 1000)
  setWriteToken('')
  persist()
  stopWatch()
}

/** 写入内存 + http 头 + sessionStorage；无效组合一律清空 */
function setAuth(token, expiresAt) {
  const tok = String(token || '').trim()
  const exp = Number(expiresAt) || 0
  const now = Math.floor(Date.now() / 1000)
  if (!tok || exp <= now) {
    clearAuth()
    return false
  }
  authEpoch += 1
  state.token = tok
  state.expiresAt = exp
  state.now = now
  setWriteToken(tok)
  persist()
  return true
}

function applyToken(token, expiresInSec) {
  const sec = typeof expiresInSec === 'number' && expiresInSec > 0 ? expiresInSec : 0
  if (!token || !sec) {
    clearAuth()
    return false
  }
  return setAuth(token, Math.floor(Date.now() / 1000) + sec)
}

export function logout() {
  clearAuth()
}

async function checkSession() {
  const tok = state.token
  const epoch = authEpoch
  if (!tok) return
  try {
    const data = await authApi.session(tok)
    if (epoch !== authEpoch || state.token !== tok) return
    const sec = typeof data?.expiresInSec === 'number' ? data.expiresInSec : NaN
    if (!Number.isFinite(sec) || sec <= 0) {
      logout()
      return
    }
    state.expiresAt = Math.floor(Date.now() / 1000) + sec
    state.now = Math.floor(Date.now() / 1000)
    persist()
  } catch (e) {
    if (epoch !== authEpoch || state.token !== tok) return
    // 仅服务端明确拒绝时退出；网络抖动不降级为只读
    if (e?.code === 401) logout()
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
  if (!state.token) return
  checkSession()
  expiryTimer = window.setInterval(tickExpiry, 1000)
  sessionTimer = window.setInterval(checkSession, 15000)
}

function restore() {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    if (!raw) return
    const data = JSON.parse(raw)
    const token = String(data?.token || '').trim()
    const expiresAt = Number(data?.expiresAt) || 0
    if (!setAuth(token, expiresAt)) return
    startWatch()
  } catch {
    clearAuth()
  }
}

export async function login(key) {
  const k = String(key || '').trim()
  if (!k) throw new Error('请输入密钥')
  state.busy = true
  try {
    const data = await authApi.login(k)
    if (!data?.token) throw new Error('验证失败')
    let ok = false
    if (typeof data.expiresInSec === 'number' && data.expiresInSec > 0) {
      ok = applyToken(data.token, data.expiresInSec)
    } else if (data.expiresAt) {
      ok = setAuth(data.token, data.expiresAt)
    }
    if (!ok) throw new Error('验证失败')
    startWatch()
    return data
  } finally {
    state.busy = false
  }
}

onWriteAuthLost(() => {
  logout()
})

restore()

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
