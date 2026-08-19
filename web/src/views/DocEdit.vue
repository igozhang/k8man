<script setup>
import { ref, onMounted } from 'vue'
import { useRoute, useRouter, RouterLink, onBeforeRouteLeave } from 'vue-router'
import { readApi, writeApi } from '../api'
import { asArray, errMessage } from '../utils/safe'
import { TTL, cacheGet, cacheKey, cacheLoad, invalidateDocs } from '../utils/resourceCache'

const route = useRoute()
const router = useRouter()
const error = ref('')
const loading = ref(!!route.params.id)
const form = ref({
  title: '',
  slug: '',
  category: 'general',
  tags: '',
  summary: '',
  content: '',
  pinned: false
})
const isEdit = !!route.params.id
const originalSnap = ref('')
let leaveSaved = false

function formSnap() {
  return JSON.stringify(form.value)
}

function applyDocForm(d) {
  form.value = {
    title: d?.title || '',
    slug: d?.slug || '',
    category: d?.category || 'general',
    tags: asArray(d?.tags).join(','),
    summary: d?.summary || '',
    content: d?.content || '',
    pinned: !!d?.pinned
  }
}

function isDirty() {
  if (leaveSaved) return false
  if (!isEdit) {
    return !!(form.value.title.trim() || form.value.content.trim() || form.value.summary.trim())
  }
  return originalSnap.value !== '' && formSnap() !== originalSnap.value
}

onMounted(async () => {
  if (!isEdit) {
    originalSnap.value = formSnap()
    return
  }
  const id = String(route.params.id || '')
  const key = cacheKey('doc', 'local', id)
  const hit = cacheGet(key)
  if (hit?.data?.content) applyDocForm(hit.data)
  try {
    applyDocForm(await cacheLoad(key, TTL.docs, () => readApi.getDoc(id), { force: !hit?.data?.content }))
  } catch (e) {
    error.value = errMessage(e)
  } finally {
    originalSnap.value = formSnap()
    loading.value = false
  }
})

onBeforeRouteLeave(() => {
  if (isDirty() && !confirm('有未保存的修改，确定离开？')) return false
})

async function save() {
  error.value = ''
  const payload = {
    title: form.value.title,
    slug: form.value.slug,
    category: form.value.category,
    tags: form.value.tags.split(',').map((s) => s.trim()).filter(Boolean),
    summary: form.value.summary,
    content: form.value.content,
    pinned: form.value.pinned
  }
  try {
    const d = isEdit
      ? await writeApi.updateDoc(route.params.id, payload)
      : await writeApi.createDoc(payload)
    if (!d?.id) {
      error.value = '保存成功但未返回文档 ID'
      return
    }
    leaveSaved = true
    invalidateDocs(d.id)
    router.push(`/docs/${d.id}`)
  } catch (e) {
    error.value = errMessage(e)
  }
}
</script>

<template>
  <div>
    <div class="toolbar">
      <div>
        <h1>{{ isEdit ? '编辑文档' : '新建文档' }}</h1>
        <p class="page-desc">使用 Markdown 编写运维手册。</p>
      </div>
      <div class="actions">
        <RouterLink v-if="isEdit" class="btn" :to="`/docs/${route.params.id}`">返回详情</RouterLink>
        <RouterLink class="btn" to="/docs">返回列表</RouterLink>
      </div>
    </div>
    <p v-if="loading" class="muted">正在加载文档内容…</p>
    <p v-if="error" class="error">{{ error }}</p>
    <div class="panel">
      <div class="form-row"><label>标题</label><input v-model="form.title" type="text" :disabled="loading" /></div>
      <div class="form-row"><label>Slug（可选）</label><input v-model="form.slug" type="text" :disabled="loading" /></div>
      <div class="form-row">
        <label>分类</label>
        <select v-model="form.category" :disabled="loading">
          <option value="general">general</option>
          <option value="k8s">k8s</option>
          <option value="devops">devops</option>
          <option value="monitoring">monitoring</option>
        </select>
      </div>
      <div class="form-row"><label>标签（逗号分隔）</label><input v-model="form.tags" type="text" :disabled="loading" /></div>
      <div class="form-row"><label>摘要</label><input v-model="form.summary" type="text" :disabled="loading" /></div>
      <div class="form-row"><label>正文 Markdown</label><textarea v-model="form.content" style="min-height:320px" :disabled="loading" :placeholder="loading ? '正在加载正文…' : ''"></textarea></div>
      <div class="form-row"><label><input v-model="form.pinned" type="checkbox" /> 置顶</label></div>
      <div class="actions">
        <button class="btn btn-primary" :disabled="loading" @click="save">保存</button>
        <RouterLink v-if="isEdit" class="btn" :to="`/docs/${route.params.id}`">取消</RouterLink>
        <RouterLink v-else class="btn" to="/docs">取消</RouterLink>
      </div>
    </div>
  </div>
</template>
