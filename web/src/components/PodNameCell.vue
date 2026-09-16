<script setup>
import { computed } from 'vue'
import { copyText } from '../utils/clipboard'

const props = defineProps({
  name: { type: String, default: '' },
  maxChars: { type: Number, default: 30 }
})

const full = computed(() => String(props.name || ''))

const display = computed(() => {
  const chars = Array.from(full.value)
  if (chars.length <= props.maxChars) return full.value
  return chars.slice(0, props.maxChars).join('') + '…'
})

const truncated = computed(() => Array.from(full.value).length > props.maxChars)

async function onDblClick() {
  const t = full.value
  if (!t) return
  try {
    await copyText(t)
  } catch {
    /* ignore */
  }
}
</script>

<template>
  <span
    class="pod-name-cell"
    :class="{ truncated, copyable: !!full }"
    :title="full || undefined"
    @dblclick.prevent="onDblClick"
  >{{ display || '-' }}</span>
</template>

<style scoped>
.pod-name-cell {
  display: inline-block;
  max-width: min(36ch, 100%);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: middle;
  cursor: default;
}
.pod-name-cell.copyable {
  cursor: copy;
}
</style>
