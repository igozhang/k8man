<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { RouterLink } from 'vue-router'
import { readApi, writeApi } from '../api'
import { asArray, errMessage } from '../utils/safe'
import { TTL, cacheFresh, cacheGet, cacheKey, cacheLoad, invalidateDocs } from '../utils/resourceCache'
import { canWrite } from '../stores/opsAuth'
import { DOC_CATEGORIES, docCategoryLabel, docPublicId } from '../constants/docCategories'

const list = ref([])
const q = ref('')
const category = ref('')
const error = ref('')
let searchTimer = 0

function scheduleSearch() {
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => load(), 300)
}

async function load({ force = false } = {}) {
  error.value = ''
  const qNow = q.value
  const catNow = category.value
  const key = cacheKey('docs', 'local', `${qNow || ''}::${catNow || ''}`)
  const hit = cacheGet(key)
  if (hit) list.value = asArray(hit.data)
  if (!force && cacheFresh(key, TTL.docs)) return
  try {
    const items = asArray(
      await cacheLoad(
        key,
        TTL.docs,
        () => readApi.listDocs({ q: qNow || undefined, category: catNow || undefined }),
        { force }
      )
    )
    if (q.value !== qNow || category.value !== catNow) return
    list.value = items
  } catch (e) {
    error.value = errMessage(e)
    if (!hit) list.value = []
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
      <input v-model="q" type="text" placeholder="搜索标题/摘要" @keyup.enter="load" @input="scheduleSearch" />
      <select v-model="category" @change="load">
        <option value="">全部分类</option>
        <option v-for="c in DOC_CATEGORIES" :key="c.value" :value="c.value">{{ c.label }}</option>
      </select>
      <button class="btn" @click="load">筛选</button>
    </div>
    <p v-if="error" class="error">{{ error }}</p>
    <div v-if="!list.length" class="empty">暂无文档。</div>
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
  </div>
</template>
