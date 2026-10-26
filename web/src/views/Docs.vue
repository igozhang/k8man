<script setup>
import { computed, ref, onMounted, onUnmounted } from 'vue'
import { RouterLink } from 'vue-router'
import { readApi, writeApi } from '../api'
import { asArray, errMessage } from '../utils/safe'
import {
  TTL,
  cacheFresh,
  cacheGet,
  cacheInvalidate,
  cacheKey,
  cacheLoad,
  cacheSet,
  invalidateDocs
} from '../utils/resourceCache'
import { canWrite } from '../stores/opsAuth'
import { DOC_CATEGORIES, docCategoryLabel, docPublicId } from '../constants/docCategories'

const PAGE_SIZE = 20

const list = ref([])
const q = ref('')
const category = ref('')
const page = ref(1)
const total = ref(0)
const pageSize = ref(PAGE_SIZE)
const error = ref('')
let searchTimer = 0
let loadGen = 0

const totalPages = computed(() => {
  const n = total.value
  const size = pageSize.value || PAGE_SIZE
  if (n <= 0) return 1
  return Math.max(1, Math.ceil(n / size))
})

/** Compact page buttons: 1 … mid … last */
const pageButtons = computed(() => {
  const cur = page.value
  const last = totalPages.value
  if (last <= 7) {
    return Array.from({ length: last }, (_, i) => i + 1)
  }
  const set = new Set([1, last, cur, cur - 1, cur + 1])
  if (cur <= 3) {
    set.add(2)
    set.add(3)
    set.add(4)
  }
  if (cur >= last - 2) {
    set.add(last - 1)
    set.add(last - 2)
    set.add(last - 3)
  }
  return [...set].filter((p) => p >= 1 && p <= last).sort((a, b) => a - b)
})

function docsListKey(qNow, catNow, pageNow) {
  return cacheKey('docs', 'local', `${qNow || ''}::${catNow || ''}::${pageNow}::${PAGE_SIZE}`)
}

/** Accept paged object; tolerate legacy bare array during rolling deploy. */
function normalizeListPayload(data) {
  if (Array.isArray(data)) {
    return {
      items: data,
      total: data.length,
      page: 1,
      pageSize: data.length || PAGE_SIZE
    }
  }
  if (!data || typeof data !== 'object') {
    return { items: [], total: 0, page: 1, pageSize: PAGE_SIZE }
  }
  const items = asArray(data.items)
  const totalN = typeof data.total === 'number' && data.total >= 0 ? data.total : items.length
  const pageN = typeof data.page === 'number' && data.page >= 1 ? data.page : 1
  const sizeN = typeof data.pageSize === 'number' && data.pageSize > 0 ? data.pageSize : PAGE_SIZE
  return { items, total: totalN, page: pageN, pageSize: sizeN }
}

/** Cached entry must match the requested page (reject server-clamped leftovers). */
function cacheMatchesPage(data, expectPage) {
  if (!data || typeof data !== 'object' || Array.isArray(data)) return false
  if (!Array.isArray(data.items)) return false
  if (typeof data.page === 'number' && data.page !== expectPage) return false
  return true
}

function applyPage(payload) {
  list.value = asArray(payload.items)
  total.value = payload.total
  page.value = payload.page
  pageSize.value = payload.pageSize
}

function scheduleSearch() {
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => {
    page.value = 1
    load()
  }, 300)
}

function onCategoryChange() {
  page.value = 1
  load()
}

function filterNow() {
  page.value = 1
  load()
}

function goPage(p) {
  const next = Math.min(Math.max(1, p), totalPages.value)
  if (next === page.value && list.value.length) return
  page.value = next
  load()
}

async function load({ force = false } = {}) {
  error.value = ''
  const qNow = q.value
  const catNow = category.value
  const pageNow = page.value
  const gen = ++loadGen
  const key = docsListKey(qNow, catNow, pageNow)
  let hit = cacheGet(key)
  // 钳页残留：请求页与缓存内 page 不一致时作废，避免 cacheLoad 因 TTL 仍返回脏数据
  if (hit && !cacheMatchesPage(hit.data, pageNow)) {
    cacheInvalidate(key)
    hit = null
  }
  if (hit) {
    applyPage(normalizeListPayload(hit.data))
  }
  if (!force && hit && cacheFresh(key, TTL.docs)) return

  try {
    const raw = await cacheLoad(
      key,
      TTL.docs,
      () =>
        readApi.listDocs({
          q: qNow || undefined,
          category: catNow || undefined,
          page: pageNow,
          pageSize: PAGE_SIZE
        }),
      { force }
    )
    if (gen !== loadGen) return
    if (q.value !== qNow || category.value !== catNow || page.value !== pageNow) return

    const payload = normalizeListPayload(raw)
    // 服务端钳页：纠正缓存键，避免「请求页 5 / 实为页 2」的脏缓存在总量回升后误用
    if (payload.page !== pageNow) {
      cacheSet(docsListKey(qNow, catNow, payload.page), payload)
      cacheInvalidate(key)
    }
    applyPage(payload)

    // COUNT/SELECT 竞态或删空末页：回退上一页
    if (total.value > 0 && list.value.length === 0 && page.value > 1) {
      page.value = page.value - 1
      await load({ force: true })
    }
  } catch (e) {
    if (gen !== loadGen) return
    error.value = errMessage(e)
    if (!(hit && cacheMatchesPage(hit.data, pageNow))) {
      list.value = []
      total.value = 0
    }
  }
}

async function remove(d) {
  if (!canWrite.value) return
  if (!confirm(`删除文档「${d.title}」？`)) return
  try {
    await writeApi.deleteDoc(d.id)
    invalidateDocs(d.id)
    await load({ force: true })
  } catch (e) {
    error.value = errMessage(e)
  }
}

function fmtTime(t) {
  if (!t) return ''
  return new Date(t).toLocaleString()
}

onMounted(load)

onUnmounted(() => {
  window.clearTimeout(searchTimer)
  loadGen++
})
</script>

<template>
  <div>
    <div class="toolbar">
      <div>
        <h1>运维文档</h1>
        <p class="page-desc">记录故障处理、迁移步骤与值班笔记。</p>
      </div>
      <RouterLink v-if="canWrite" class="btn btn-primary" to="/docs/edit">新建文档</RouterLink>
    </div>
    <div class="toolbar toolbar-start">
      <input v-model="q" type="text" placeholder="搜索标题/摘要" @keyup.enter="filterNow" @input="scheduleSearch" />
      <select v-model="category" @change="onCategoryChange">
        <option value="">全部分类</option>
        <option v-for="c in DOC_CATEGORIES" :key="c.value" :value="c.value">{{ c.label }}</option>
      </select>
      <button class="btn" @click="filterNow">筛选</button>
    </div>
    <p v-if="error" class="error">{{ error }}</p>
    <div v-if="!list.length && total === 0" class="empty">暂无文档。</div>
    <div v-for="d in list" :key="d.id" class="doc-capsule">
      <div class="doc-capsule-head">
        <h2 class="doc-capsule-title"><RouterLink :to="`/docs/${docPublicId(d)}`">{{ d.title }}</RouterLink></h2>
        <span class="doc-capsule-head-meta">
          <span class="tag">{{ docCategoryLabel(d.category) }}</span>
          <span v-if="d.pinned" class="tag current">置顶</span>
          <span>{{ fmtTime(d.updatedAt) }}</span>
        </span>
      </div>
      <p class="doc-capsule-excerpt">{{ d.summary || '（无摘要）' }}</p>
      <div v-if="canWrite" class="actions">
        <RouterLink class="btn btn-sm" :to="`/docs/edit/${docPublicId(d)}`">编辑</RouterLink>
        <button class="btn btn-sm btn-danger" @click="remove(d)">删除</button>
      </div>
    </div>
    <div v-if="total > 0" class="toolbar toolbar-start pager">
      <span class="muted">共 {{ total }} 篇</span>
      <button class="btn" type="button" :disabled="page <= 1" @click="goPage(page - 1)">上一页</button>
      <template v-for="(p, i) in pageButtons" :key="p">
        <span v-if="i > 0 && p - pageButtons[i - 1] > 1" class="pager-ellipsis muted">…</span>
        <button
          class="btn"
          type="button"
          :class="{ 'is-current': p === page }"
          :disabled="p === page"
          @click="goPage(p)"
        >{{ p }}</button>
      </template>
      <button class="btn" type="button" :disabled="page >= totalPages" @click="goPage(page + 1)">下一页</button>
    </div>
  </div>
</template>
