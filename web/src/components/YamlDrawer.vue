<script setup>
import { ref, watch } from 'vue'
import { readApi, writeApi } from '../api'
import { errMessage } from '../utils/safe'
import { copyText } from '../utils/clipboard'

const props = defineProps({
  clusterId: { type: String, default: '' },
  ns: { type: String, default: '' },
  readOnly: { type: Boolean, default: false }
})

const emit = defineEmits(['saved', 'error'])

const open = ref(false)
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
  try {
    const r = await readApi.getYAML(props.clusterId, ns, resourceKind, resourceName)
    kind.value = resourceKind
    name.value = resourceName
    activeNs.value = ns
    yaml.value = r?.yaml || ''
    open.value = true
  } catch (e) {
    emit('error', errMessage(e))
  }
}

function close() {
  open.value = false
  activeNs.value = ''
}

async function save() {
  if (props.readOnly) return
  busy.value = true
  try {
    await writeApi.applyYAML(props.clusterId, activeNs.value || props.ns, kind.value, name.value, yaml.value)
    open.value = false
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
          :disabled="busy"
          @click="save"
        >
          {{ busy ? '应用中…' : '应用' }}
        </button>
        <button class="btn btn-sm" @click="close">关闭</button>
      </div>
    </div>
    <div class="field-copy-wrap">
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
