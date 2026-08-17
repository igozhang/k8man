<script setup>
import { ref, onMounted } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { readApi, writeApi } from '../api'
import { asArray, errMessage } from '../utils/safe'

const route = useRoute()
const router = useRouter()
const error = ref('')
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

onMounted(async () => {
  if (!isEdit) return
  try {
    const d = await readApi.getDoc(route.params.id)
    form.value = {
      title: d?.title || '',
      slug: d?.slug || '',
      category: d?.category || 'general',
      tags: asArray(d?.tags).join(','),
      summary: d?.summary || '',
      content: d?.content || '',
      pinned: !!d?.pinned
    }
  } catch (e) {
    error.value = errMessage(e)
  }
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
    router.push(`/docs/${d.id}`)
  } catch (e) {
    error.value = errMessage(e)
  }
}
</script>

<template>
  <div>
    <h1>{{ isEdit ? '编辑文档' : '新建文档' }}</h1>
    <p class="page-desc">使用 Markdown 编写运维手册。</p>
    <p v-if="error" class="error">{{ error }}</p>
    <div class="panel">
      <div class="form-row"><label>标题</label><input v-model="form.title" type="text" /></div>
      <div class="form-row"><label>Slug（可选）</label><input v-model="form.slug" type="text" /></div>
      <div class="form-row">
        <label>分类</label>
        <select v-model="form.category">
          <option value="general">general</option>
          <option value="k8s">k8s</option>
          <option value="devops">devops</option>
          <option value="monitoring">monitoring</option>
        </select>
      </div>
      <div class="form-row"><label>标签（逗号分隔）</label><input v-model="form.tags" type="text" /></div>
      <div class="form-row"><label>摘要</label><input v-model="form.summary" type="text" /></div>
      <div class="form-row"><label>正文 Markdown</label><textarea v-model="form.content" style="min-height:320px"></textarea></div>
      <div class="form-row"><label><input v-model="form.pinned" type="checkbox" /> 置顶</label></div>
      <div class="actions">
        <button class="btn btn-primary" @click="save">保存</button>
        <RouterLink class="btn" to="/docs">取消</RouterLink>
      </div>
    </div>
  </div>
</template>
