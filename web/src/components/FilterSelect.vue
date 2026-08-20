<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'

const props = defineProps({
  modelValue: { type: String, default: '' },
  /** string[] 或 { value|name, label? }[] */
  options: { type: Array, default: () => [] },
  /** 空选项文案；有则允许选空，无则必须选中某个选项 */
  emptyLabel: { type: String, default: '' },
  placeholder: { type: String, default: '输入 1–3 个字母筛选' },
  ariaLabel: { type: String, default: '选择命名空间' }
})

const emit = defineEmits(['update:modelValue'])

const open = ref(false)
const query = ref('')
const highlight = ref(0)
const rootRef = ref(null)
const inputRef = ref(null)
const listRef = ref(null)

const items = computed(() => {
  const out = []
  const seen = new Set()
  for (const o of props.options || []) {
    if (o == null) continue
    let value = ''
    let label = ''
    if (typeof o === 'string') {
      value = o.trim()
      label = value
    } else {
      value = String(o.value ?? o.name ?? '').trim()
      label = String(o.label ?? o.name ?? value).trim() || value
    }
    if (!value || seen.has(value)) continue
    seen.add(value)
    out.push({ value, label })
  }
  return out
})

const allowEmpty = computed(() => !!String(props.emptyLabel || '').trim())

const selectedInList = computed(() => {
  if (!props.modelValue) return true
  return items.value.some((it) => it.value === props.modelValue)
})

const selectedLabel = computed(() => {
  if (!props.modelValue) return props.emptyLabel || ''
  const hit = items.value.find((it) => it.value === props.modelValue)
  return hit?.label || props.modelValue
})

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  const list = items.value
  if (!q) return list
  const scored = []
  for (const it of list) {
    const label = it.label.toLowerCase()
    const value = it.value.toLowerCase()
    let score = 0
    if (value === q || label === q) score = 300
    else if (value.startsWith(q) || label.startsWith(q)) score = 200
    else if (value.includes(q) || label.includes(q)) score = 100
    else continue
    if (q.length <= 3 && score >= 200) score += 20
    scored.push({ it, score })
  }
  scored.sort((a, b) => b.score - a.score || a.it.label.localeCompare(b.it.label, 'zh'))
  return scored.map((s) => s.it)
})

const showEmptyOption = computed(() => allowEmpty.value && !query.value.trim())

const hintText = computed(() => {
  if (!open.value) return ''
  const cur = props.modelValue ? `当前 ${selectedLabel.value}` : allowEmpty.value ? `当前 ${props.emptyLabel}` : '尚未选择'
  if (!filtered.value.length) return `${cur} · 无匹配，Esc 取消`
  if (filtered.value.length === 1) return `${cur} · 仅 1 项，回车可选中`
  return `${cur} · 回车选中高亮项 · Esc 取消`
})

/** 选项失效时自动清空（仅允许空值时），避免带着无效 ns 继续请求 */
watch(
  [items, () => props.modelValue, allowEmpty],
  () => {
    const v = String(props.modelValue || '').trim()
    if (!v) return
    // 列表尚未就绪时不清空，避免把刚恢复的偏好/外部赋值误清掉
    if (!items.value.length) return
    if (items.value.some((it) => it.value === v)) return
    if (allowEmpty.value) emit('update:modelValue', '')
  },
  { immediate: true }
)

watch(
  () => props.modelValue,
  () => {
    if (!open.value) query.value = ''
  }
)

watch(filtered, (list) => {
  if (!open.value) return
  if (!list.length) {
    highlight.value = showEmptyOption.value ? -1 : 0
    return
  }
  if (highlight.value >= list.length) highlight.value = 0
  if (highlight.value < 0 && !showEmptyOption.value) highlight.value = 0
})

function openList() {
  open.value = true
  query.value = ''
  if (props.modelValue) {
    const idx = filtered.value.findIndex((it) => it.value === props.modelValue)
    highlight.value = idx >= 0 ? idx : 0
  } else {
    highlight.value = showEmptyOption.value ? -1 : 0
  }
  nextTick(() => {
    inputRef.value?.focus()
    inputRef.value?.select?.()
  })
}

/** 关闭面板不改 modelValue：误触 / Esc / 点外面都防呆保留原选择 */
function cancelClose() {
  open.value = false
  query.value = ''
  highlight.value = 0
}

function pick(value) {
  const v = String(value || '').trim()
  if (!v) {
    if (!allowEmpty.value) return
    if (props.modelValue !== '') emit('update:modelValue', '')
    cancelClose()
    return
  }
  // 防呆：禁止自由输入不在列表中的值
  if (!items.value.some((it) => it.value === v)) return
  if (props.modelValue !== v) emit('update:modelValue', v)
  cancelClose()
}

function onInput() {
  open.value = true
  if (filtered.value.length) highlight.value = 0
  else highlight.value = showEmptyOption.value ? -1 : 0
}

function resolveEnterTarget() {
  const q = query.value.trim().toLowerCase()
  if (q) {
    const exact = filtered.value.find((it) => it.value.toLowerCase() === q || it.label.toLowerCase() === q)
    if (exact) return exact.value
    if (filtered.value.length === 1) return filtered.value[0].value
  }
  if (highlight.value === -1 && showEmptyOption.value) return ''
  const hit = filtered.value[highlight.value]
  return hit ? hit.value : null
}

function onKeydown(e) {
  // 输入法组字中回车只上屏，不触发选中，避免误选
  if (e.isComposing || e.keyCode === 229) return
  if (e.key === 'Escape') {
    e.preventDefault()
    cancelClose()
    return
  }
  if (e.key === 'Tab') {
    // 让焦点正常移走，同时收起面板且不改当前值
    cancelClose()
    return
  }
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    if (!open.value) openList()
    else moveHighlight(1)
    return
  }
  if (e.key === 'ArrowUp') {
    e.preventDefault()
    if (!open.value) openList()
    else moveHighlight(-1)
    return
  }
  if (e.key === 'Enter') {
    e.preventDefault()
    if (!open.value) {
      openList()
      return
    }
    const target = resolveEnterTarget()
    if (target == null) return
    pick(target)
  }
}

function moveHighlight(delta) {
  const n = filtered.value.length
  const hasEmpty = showEmptyOption.value
  const min = hasEmpty ? -1 : 0
  const max = n - 1
  if (max < min) {
    highlight.value = min
    return
  }
  let next = highlight.value + delta
  if (next < min) next = max
  if (next > max) next = min
  highlight.value = next
  nextTick(scrollHighlightIntoView)
}

function scrollHighlightIntoView() {
  const root = listRef.value
  if (!root) return
  const el = root.querySelector('[data-active="1"]')
  el?.scrollIntoView({ block: 'nearest' })
}

function onDocPointerDown(e) {
  if (!open.value) return
  if (rootRef.value?.contains(e.target)) return
  cancelClose()
}

onMounted(() => document.addEventListener('pointerdown', onDocPointerDown))
onUnmounted(() => document.removeEventListener('pointerdown', onDocPointerDown))
</script>

<template>
  <div ref="rootRef" class="filter-select" :class="{ open, stale: modelValue && !selectedInList }">
    <button
      v-if="!open"
      type="button"
      class="filter-select-trigger"
      :aria-label="ariaLabel"
      aria-haspopup="listbox"
      :aria-expanded="false"
      @click="openList"
    >
      <span class="filter-select-value" :class="{ muted: !modelValue }">
        {{ selectedLabel || placeholder }}
        <template v-if="modelValue && !selectedInList">（已失效）</template>
      </span>
      <span class="filter-select-caret" aria-hidden="true">▾</span>
    </button>
    <input
      v-else
      ref="inputRef"
      v-model="query"
      class="filter-select-input"
      type="text"
      autocomplete="off"
      spellcheck="false"
      :placeholder="placeholder"
      :aria-label="ariaLabel"
      aria-autocomplete="list"
      :aria-expanded="true"
      @input="onInput"
      @keydown="onKeydown"
    />
    <div v-if="open" class="filter-select-dropdown">
      <p class="filter-select-hint muted">{{ hintText }}</p>
      <ul ref="listRef" class="filter-select-list" role="listbox">
        <li
          v-if="showEmptyOption"
          role="option"
          class="filter-select-option"
          :class="{ active: highlight === -1 }"
          :data-active="highlight === -1 ? '1' : '0'"
          :aria-selected="!modelValue"
          @mousedown.prevent="pick('')"
        >{{ emptyLabel }}</li>
        <li
          v-for="(it, idx) in filtered"
          :key="it.value"
          role="option"
          class="filter-select-option"
          :class="{ active: highlight === idx, current: modelValue === it.value }"
          :data-active="highlight === idx ? '1' : '0'"
          :aria-selected="modelValue === it.value"
          @mousedown.prevent="pick(it.value)"
        >{{ it.label }}</li>
        <li v-if="!filtered.length" class="filter-select-empty muted">无匹配项，按 Esc 取消</li>
      </ul>
    </div>
  </div>
</template>
