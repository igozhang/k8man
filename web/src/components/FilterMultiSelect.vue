<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'

const props = defineProps({
  /** 已选 value 数组；空数组 = 不过滤（显示全部） */
  modelValue: { type: Array, default: () => [] },
  /** string[] 或 { value|name, label? }[] */
  options: { type: Array, default: () => [] },
  placeholder: { type: String, default: '输入筛选' },
  emptyLabel: { type: String, default: '全部（未筛选）' },
  ariaLabel: { type: String, default: '多选筛选' },
  /** 未选时 trigger 文案前缀 */
  allLabel: { type: String, default: '全部' },
  /** 禁用时占位但不展开（避免 v-if 卸载造成布局跳动） */
  disabled: { type: Boolean, default: false },
  disabledTitle: { type: String, default: '' }
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

const selectedSet = computed(() => new Set((props.modelValue || []).map((v) => String(v))))

const selectedCount = computed(() => {
  let n = 0
  for (const it of items.value) {
    if (selectedSet.value.has(it.value)) n++
  }
  return n
})

const triggerLabel = computed(() => {
  const total = items.value.length
  const n = selectedCount.value
  const raw = (props.modelValue || []).length
  // options 尚未加载/失败时仍用 modelValue 提示，避免看起来像「全部」却在过滤
  if (!n && !raw) return props.allLabel
  if (!n && raw) return `已选 ${raw}`
  if (n === total && total > 0) return `已全选 ${n}`
  return `已选 ${n}`
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
    scored.push({ it, score })
  }
  scored.sort((a, b) => b.score - a.score || a.it.label.localeCompare(b.it.label, 'zh'))
  return scored.map((s) => s.it)
})

const hintText = computed(() => {
  if (!open.value) return ''
  const cur = selectedCount.value ? `已选 ${selectedCount.value}` : props.emptyLabel
  if (!filtered.value.length) return `${cur} · 无匹配`
  return `${cur} · 勾选筛选 · 空选=全部 · Esc 关闭`
})

watch(
  [items, () => props.modelValue],
  () => {
    const cur = (props.modelValue || []).map((v) => String(v))
    if (!cur.length) return
    if (!items.value.length) return
    const valid = new Set(items.value.map((it) => it.value))
    const next = cur.filter((v) => valid.has(v))
    if (next.length !== cur.length) emit('update:modelValue', next)
  },
  { immediate: true }
)

watch(filtered, (list) => {
  if (!open.value) return
  if (!list.length) {
    highlight.value = 0
    return
  }
  if (highlight.value >= list.length) highlight.value = 0
})

function cancelClose() {
  open.value = false
  query.value = ''
  highlight.value = 0
}

function openList() {
  if (props.disabled) return
  open.value = true
  query.value = ''
  highlight.value = 0
  nextTick(() => {
    inputRef.value?.focus()
    inputRef.value?.select?.()
  })
}

watch(
  () => props.disabled,
  (off) => {
    if (off && open.value) cancelClose()
  }
)

function emitSelection(next) {
  if (props.disabled) return
  const uniq = []
  const seen = new Set()
  for (const v of next) {
    const s = String(v || '').trim()
    if (!s || seen.has(s)) continue
    seen.add(s)
    uniq.push(s)
  }
  const cur = (props.modelValue || []).map(String)
  if (uniq.length === cur.length && uniq.every((v, i) => v === cur[i])) return
  emit('update:modelValue', uniq)
}

function toggle(value) {
  if (props.disabled) return
  const v = String(value || '').trim()
  if (!v) return
  if (!items.value.some((it) => it.value === v)) return
  const set = new Set(selectedSet.value)
  if (set.has(v)) set.delete(v)
  else set.add(v)
  emitSelection([...set])
}

function clearAll() {
  if (props.disabled) return
  emitSelection([])
}

function selectVisible() {
  if (props.disabled) return
  const set = new Set(selectedSet.value)
  for (const it of filtered.value) set.add(it.value)
  emitSelection([...set])
}

function onInput() {
  if (props.disabled) {
    cancelClose()
    return
  }
  open.value = true
  highlight.value = 0
}

function onKeydown(e) {
  if (e.isComposing || e.keyCode === 229) return
  if (props.disabled) {
    e.preventDefault()
    cancelClose()
    return
  }
  if (e.key === 'Escape') {
    e.preventDefault()
    cancelClose()
    return
  }
  if (e.key === 'Tab') {
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
  // 空格留给搜索输入；仅回车切换勾选
  if (e.key === 'Enter') {
    if (!open.value) {
      e.preventDefault()
      openList()
      return
    }
    const hit = filtered.value[highlight.value]
    if (!hit) return
    e.preventDefault()
    toggle(hit.value)
  }
}

function moveHighlight(delta) {
  const n = filtered.value.length
  if (!n) {
    highlight.value = 0
    return
  }
  let next = highlight.value + delta
  if (next < 0) next = n - 1
  if (next >= n) next = 0
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
  <div
    ref="rootRef"
    class="filter-select filter-multi"
    :class="{ open, 'is-disabled': disabled }"
  >
    <button
      v-if="!open"
      type="button"
      class="filter-select-trigger"
      :aria-label="ariaLabel"
      aria-haspopup="listbox"
      :aria-expanded="false"
      :disabled="disabled"
      :title="disabled ? disabledTitle : undefined"
      @click="openList"
    >
      <span class="filter-select-value" :class="{ muted: !selectedCount || disabled }">{{ triggerLabel }}</span>
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
      <div class="filter-multi-actions">
        <button type="button" class="btn btn-sm" @mousedown.prevent="selectVisible">全选可见</button>
        <button type="button" class="btn btn-sm" @mousedown.prevent="clearAll">清空（全部）</button>
      </div>
      <ul ref="listRef" class="filter-select-list" role="listbox" aria-multiselectable="true">
        <li
          v-for="(it, idx) in filtered"
          :key="it.value"
          role="option"
          class="filter-select-option filter-multi-option"
          :class="{ active: highlight === idx, current: selectedSet.has(it.value) }"
          :data-active="highlight === idx ? '1' : '0'"
          :aria-selected="selectedSet.has(it.value)"
          @mousedown.prevent="toggle(it.value)"
        >
          <input
            class="filter-multi-check"
            type="checkbox"
            tabindex="-1"
            :checked="selectedSet.has(it.value)"
            @click.prevent
          />
          <span>{{ it.label }}</span>
        </li>
        <li v-if="!filtered.length" class="filter-select-empty muted">无匹配项</li>
      </ul>
    </div>
  </div>
</template>
