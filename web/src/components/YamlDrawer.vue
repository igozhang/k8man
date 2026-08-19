<script setup>
import { ref, watch } from 'vue'
import { readApi, writeApi } from '../api'
import { createSeq, errMessage } from '../utils/safe'
import { copyText } from '../utils/clipboard'

const props = defineProps({
  clusterId: { type: String, default: '' },
  ns: { type: String, default: '' },
  readOnly: { type: Boolean, default: false }
})

const emit = defineEmits(['saved', 'error'])

const openSeq = createSeq()
const open = ref(false)
const loading = ref(false)
const loadError = ref('')
const kind = ref('')
const name = ref('')
const activeNs = ref('')
const yaml = ref('')
const busy = ref(false)
const copied = ref(false)
let copiedTimer = 0

watch(
  () => [props.clusterId, props.ns],
  () => {
    if (open.value) close()
  }
)

async function openEditor(resourceKind, resourceName, resourceNs) {
  const ns = resourceNs || props.ns
  const seq = openSeq.next()
  kind.value = resourceKind
  name.value = resourceName
  activeNs.value = ns
  yaml.value = ''
  loadError.value = ''
  loading.value = true
  busy.value = false
  open.value = true
  try {
    const r = await readApi.getYAML(props.clusterId, ns, resourceKind, resourceName)
    if (!openSeq.is(seq)) return
    yaml.value = r?.yaml ?? ''
    if (r?.yaml == null && yaml.value === '') loadError.value = '未读到 YAML 内容'
  } catch (e) {
    if (!openSeq.is(seq)) return
    loadError.value = errMessage(e)
    emit('error', loadError.value)
  } finally {
    if (openSeq.is(seq)) loading.value = false
  }
}

function close() {
  openSeq.bump()
  open.value = false
  loading.value = false
  loadError.value = ''
  activeNs.value = ''
  yaml.value = ''
}

async function save() {
  if (props.readOnly) return
  busy.value = true
  try {
    await writeApi.applyYAML(props.clusterId, activeNs.value || props.ns, kind.value, name.value, yaml.value)
    close()
    emit('saved')
  } catch (e) {
    emit('error', errMessage(e))
  } finally {
    busy.value = false
  }
}

async function onCopy() {
  try {
    await copyText(yaml.value)
    copied.value = true
    window.clearTimeout(copiedTimer)
    copiedTimer = window.setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    alert('复制失败')
  }
}

defineExpose({ open: openEditor, close })
</script>

<template>
  <div v-if="open" class="drawer-backdrop" @click="close"></div>
  <div v-if="open" class="drawer drawer-wide">
    <div class="toolbar">
      <strong>YAML · {{ kind }}/{{ name }}</strong>
      <div class="actions">
        <button
          v-if="!readOnly"
          class="btn btn-primary btn-sm"
          :disabled="busy || loading || !!loadError"
          @click="save"
        >
          {{ busy ? '应用中…' : '应用' }}
        </button>
        <button class="btn btn-sm" @click="close">关闭</button>
      </div>
    </div>
    <p v-if="loading" class="muted">正在加载 YAML…</p>
    <p v-else-if="loadError" class="error">{{ loadError }}</p>
    <div v-else class="field-copy-wrap">
      <textarea v-model="yaml" class="cm-yaml" :readonly="readOnly"></textarea>
      <button
        type="button"
        class="field-copy"
        :class="{ 'is-copied': copied }"
        @click="onCopy"
      >{{ copied ? '已复制' : '复制' }}</button>
    </div>
  </div>
</template>
