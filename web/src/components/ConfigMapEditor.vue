<script setup>
import { ref, nextTick, onUnmounted, watch } from 'vue'
import { readApi, writeApi } from '../api'
import { asArray, createSeq, errMessage } from '../utils/safe'
import { copyText } from '../utils/clipboard'

const props = defineProps({
  clusterId: { type: String, default: '' },
  ns: { type: String, default: '' },
  readOnly: { type: Boolean, default: false }
})

const emit = defineEmits(['saved', 'error'])

const CM_VAL_MIN_PX = 120
const CM_VAL_MAX_VH = 0.55
const CM_VAL_MAX_PX = 560

const openSeq = createSeq()
const open = ref(false)
const loading = ref(false)
const name = ref('')
const mode = ref('kv') // kv | yaml
const entries = ref([])
const binaryKeys = ref([])
const yaml = ref('')
const busy = ref(false)
const localError = ref('')
/** 复制反馈：'yaml' | `v:${index}` | '' */
const copied = ref('')
let copiedTimer = 0

watch(
  () => [props.clusterId, props.ns],
  () => {
    if (open.value) close()
  }
)

function dataToEntries(data) {
  if (!data || typeof data !== 'object' || Array.isArray(data)) return []
  return Object.keys(data)
    .sort()
    .map((key) => ({ key, value: data[key] ?? '' }))
}

function entriesToData(list) {
  const data = {}
  for (const e of asArray(list)) {
    const k = String(e.key || '').trim()
    if (!k) continue
    if (Object.prototype.hasOwnProperty.call(data, k)) {
      throw new Error(`重复键：${k}`)
    }
    data[k] = e.value ?? ''
  }
  return data
}

function cmValMaxPx() {
  return Math.round(Math.min(window.innerHeight * CM_VAL_MAX_VH, CM_VAL_MAX_PX))
}

function fitCmValue(el) {
  if (!el) return
  const max = cmValMaxPx()
  el.style.height = '0px'
  const content = el.scrollHeight
  const next = Math.min(max, Math.max(CM_VAL_MIN_PX, content))
  el.style.height = `${next}px`
  el.style.overflowY = content > max ? 'auto' : 'hidden'
}

function fitAllCmValues() {
  nextTick(() => {
    document.querySelectorAll('.cm-kv-row textarea.kv-val').forEach(fitCmValue)
  })
}

async function markCopied(token) {
  try {
    copied.value = token
    window.clearTimeout(copiedTimer)
    copiedTimer = window.setTimeout(() => {
      if (copied.value === token) copied.value = ''
    }, 1500)
  } catch {
    /* ignore */
  }
}

async function copyValue(i) {
  const e = entries.value[i]
  if (!e) return
  try {
    await copyText(e.value ?? '')
    await markCopied(`v:${i}`)
  } catch {
    alert('复制失败')
  }
}

async function copyYaml() {
  try {
    await copyText(yaml.value || '')
    await markCopied('yaml')
  } catch {
    alert('复制失败')
  }
}

async function openEditor(cmName) {
  const seq = openSeq.next()
  localError.value = ''
  copied.value = ''
  name.value = cmName
  mode.value = 'kv'
  entries.value = []
  binaryKeys.value = []
  yaml.value = ''
  busy.value = false
  loading.value = true
  open.value = true
  try {
    const detail = await readApi.getConfigMap(props.clusterId, props.ns, cmName)
    if (!openSeq.is(seq)) return
    entries.value = dataToEntries(detail?.data)
    binaryKeys.value = asArray(detail?.binaryKeys)
    fitAllCmValues()
  } catch (e) {
    if (!openSeq.is(seq)) return
    localError.value = errMessage(e)
    emit('error', localError.value)
  } finally {
    if (openSeq.is(seq)) loading.value = false
  }
}

function close() {
  openSeq.bump()
  open.value = false
  loading.value = false
}

function addEntry() {
  entries.value.push({ key: '', value: '' })
  fitAllCmValues()
}

function removeEntry(i) {
  entries.value.splice(i, 1)
}

async function switchMode(next) {
  if (loading.value || next === mode.value) return
  if (
    !confirm(
      next === 'yaml'
        ? '切换到 YAML 会丢弃未保存的键值修改，并从集群重新加载。继续？'
        : '切换到键值会丢弃未保存的 YAML 修改，并从集群重新加载。继续？'
    )
  ) {
    return
  }
  localError.value = ''
  if (next === 'yaml') {
    try {
      const r = await readApi.getYAML(props.clusterId, props.ns, 'configmap', name.value)
      yaml.value = r?.yaml || ''
      mode.value = 'yaml'
    } catch (e) {
      localError.value = errMessage(e)
      emit('error', errMessage(e))
    }
    return
  }
  try {
    const detail = await readApi.getConfigMap(props.clusterId, props.ns, name.value)
    entries.value = dataToEntries(detail?.data)
    binaryKeys.value = asArray(detail?.binaryKeys)
    mode.value = 'kv'
    fitAllCmValues()
  } catch (e) {
    localError.value = errMessage(e)
    emit('error', errMessage(e))
  }
}

async function save() {
  if (props.readOnly) return
  localError.value = ''
  busy.value = true
  try {
    if (mode.value === 'yaml') {
      await writeApi.applyYAML(props.clusterId, props.ns, 'configmap', name.value, yaml.value)
    } else {
      const data = entriesToData(entries.value)
      await writeApi.patchConfigMapData(props.clusterId, props.ns, name.value, data)
    }
    close()
    emit('saved')
  } catch (e) {
    localError.value = errMessage(e)
    emit('error', errMessage(e))
  } finally {
    busy.value = false
  }
}

onUnmounted(() => {
  window.clearTimeout(copiedTimer)
})

defineExpose({ open: openEditor, close })
</script>

<template>
  <div v-if="open" class="drawer-backdrop" @click="close"></div>
  <div v-if="open" class="drawer drawer-wide">
    <div class="toolbar">
      <strong>ConfigMap · {{ name }}</strong>
      <div class="actions">
        <div class="tabs">
          <button
            type="button"
            class="tab"
            :class="{ active: mode === 'kv' }"
            :disabled="loading"
            @click="switchMode('kv')"
          >键值</button>
          <button
            type="button"
            class="tab"
            :class="{ active: mode === 'yaml' }"
            :disabled="loading"
            @click="switchMode('yaml')"
          >YAML</button>
        </div>
        <button
          v-if="!readOnly"
          class="btn btn-primary btn-sm"
          :disabled="busy || loading"
          @click="save"
        >
          {{ busy ? '保存中…' : '保存' }}
        </button>
        <button class="btn btn-sm" @click="close">关闭</button>
      </div>
    </div>
    <p v-if="loading" class="muted">正在加载 ConfigMap…</p>
    <p v-if="localError" class="error">{{ localError }}</p>
    <p v-if="!loading && binaryKeys.length" class="muted">
      含二进制键（不可在键值表编辑）：{{ binaryKeys.join(', ') }}
    </p>
    <template v-if="!loading && mode === 'kv'">
      <div class="kv-list cm-kv-list">
        <div v-for="(e, i) in entries" :key="i" class="kv-row cm-kv-row">
          <input v-model="e.key" placeholder="key" class="kv-key" :readonly="readOnly" />
          <div class="cm-kv-actions">
            <button v-if="!readOnly" class="btn btn-sm btn-danger" @click="removeEntry(i)">删</button>
          </div>
          <div class="cm-kv-val-wrap field-copy-wrap">
            <textarea
              v-model="e.value"
              placeholder="value"
              class="kv-val"
              rows="4"
              :readonly="readOnly"
              @input="fitCmValue($event.target)"
            ></textarea>
            <button
              type="button"
              class="field-copy"
              :class="{ 'is-copied': copied === 'v:' + i }"
              @click="copyValue(i)"
            >{{ copied === 'v:' + i ? '已复制' : '复制' }}</button>
          </div>
        </div>
      </div>
      <button v-if="!readOnly" class="btn btn-sm" @click="addEntry">添加键</button>
    </template>
    <div v-else-if="!loading && mode === 'yaml'" class="field-copy-wrap">
      <textarea v-model="yaml" class="cm-yaml" :readonly="readOnly"></textarea>
      <button
        type="button"
        class="field-copy"
        :class="{ 'is-copied': copied === 'yaml' }"
        @click="copyYaml"
      >{{ copied === 'yaml' ? '已复制' : '复制' }}</button>
    </div>
  </div>
</template>
