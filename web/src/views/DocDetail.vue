<script setup>
import { ref, watch, computed } from 'vue'
import { useRoute, RouterLink } from 'vue-router'
import { marked } from 'marked'
import { readApi } from '../api'
import { errMessage } from '../utils/safe'
import { TTL, cacheGet, cacheKey, cacheLoad } from '../utils/resourceCache'
import { canWrite } from '../stores/opsAuth'

const route = useRoute()
const doc = ref(null)
const error = ref('')
const html = computed(() => {
  try {
    return doc.value?.content ? marked.parse(doc.value.content) : ''
  } catch {
    return '<p class="error">文档内容解析失败</p>'
  }
})

let docSeq = 0

async function load() {
  const seq = ++docSeq
  const docId = String(route.params.id || '')
  error.value = ''
  if (!docId) {
    doc.value = null
    return
  }
  const docKey = cacheKey('doc', 'local', docId)
  const hit = cacheGet(docKey)
  if (hit?.data) doc.value = hit.data
  try {
    const data = await cacheLoad(docKey, TTL.docs, () => readApi.getDoc(docId))
    if (seq !== docSeq) return
    doc.value = data
  } catch (e) {
    if (seq !== docSeq) return
    error.value = errMessage(e)
    if (!hit?.data) doc.value = null
  }
}

watch(() => String(route.params.id || ''), load, { immediate: true })
</script>

<template>
  <div>
    <p v-if="error" class="error">{{ error }}</p>
    <template v-else-if="doc">
      <div class="toolbar">
        <div>
          <div class="doc-meta">
            <span class="tag">{{ doc.category }}</span>
            <span v-if="doc.pinned" class="tag current">置顶</span>
            <span>{{ new Date(doc.updatedAt).toLocaleString() }}</span>
          </div>
          <h1>{{ doc.title }}</h1>
        </div>
        <RouterLink v-if="canWrite" class="btn" :to="`/docs/edit/${doc.id}`">编辑</RouterLink>
      </div>
      <article class="panel markdown-body" v-html="html"></article>
      <p><RouterLink to="/docs">← 返回列表</RouterLink></p>
    </template>
  </div>
</template>
