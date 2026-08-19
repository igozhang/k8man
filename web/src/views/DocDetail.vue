<script setup>
import { ref, onMounted, computed } from 'vue'
import { useRoute, RouterLink } from 'vue-router'
import { marked } from 'marked'
import { readApi } from '../api'
import { errMessage } from '../utils/safe'

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

onMounted(async () => {
  try {
    doc.value = await readApi.getDoc(route.params.id)
  } catch (e) {
    error.value = errMessage(e)
    doc.value = null
  }
})
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
        <RouterLink class="btn" :to="`/docs/edit/${doc.id}`">编辑</RouterLink>
      </div>
      <article class="panel markdown-body" v-html="html"></article>
      <p><RouterLink to="/docs">← 返回列表</RouterLink></p>
    </template>
  </div>
</template>
