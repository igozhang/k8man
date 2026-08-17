<script setup>
import { ref, onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import { readApi, writeApi } from '../api'
import { asArray, errMessage } from '../utils/safe'

const list = ref([])
const q = ref('')
const category = ref('')
const error = ref('')

async function load() {
  error.value = ''
  try {
    list.value = asArray(
      await readApi.listDocs({ q: q.value || undefined, category: category.value || undefined })
    )
  } catch (e) {
    error.value = errMessage(e)
    list.value = []
  }
}

async function remove(d) {
  if (!confirm(`删除文档「${d.title}」？`)) return
  try {
    await writeApi.deleteDoc(d.id)
    await load()
  } catch (e) {
    error.value = errMessage(e)
  }
}

function fmtTime(t) {
  if (!t) return ''
  return new Date(t).toLocaleString()
}

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <div>
        <h1>运维文档</h1>
        <p class="page-desc">记录故障处理、迁移步骤与值班笔记。</p>
      </div>
      <RouterLink class="btn btn-primary" to="/docs/edit">新建文档</RouterLink>
    </div>
    <div class="toolbar">
      <input v-model="q" type="text" placeholder="搜索标题/摘要" style="max-width:260px" @keyup.enter="load" />
      <select v-model="category" style="max-width:160px" @change="load">
        <option value="">全部分类</option>
        <option value="k8s">k8s</option>
        <option value="devops">devops</option>
        <option value="monitoring">monitoring</option>
        <option value="general">general</option>
      </select>
      <button class="btn" @click="load">筛选</button>
    </div>
    <p v-if="error" class="error">{{ error }}</p>
    <div v-if="!list.length" class="empty">暂无文档。</div>
    <div v-for="d in list" :key="d.id" class="doc-item">
      <div class="doc-meta">
        <span class="tag">{{ d.category }}</span>
        <span v-if="d.pinned"> · 置顶</span>
        · {{ fmtTime(d.updatedAt) }}
      </div>
      <h2><RouterLink :to="`/docs/${d.id}`">{{ d.title }}</RouterLink></h2>
      <div class="doc-summary">{{ d.summary || '（无摘要）' }}</div>
      <div class="actions">
        <RouterLink class="btn btn-sm" :to="`/docs/edit/${d.id}`">编辑</RouterLink>
        <button class="btn btn-sm btn-danger" @click="remove(d)">删除</button>
      </div>
    </div>
  </div>
</template>
