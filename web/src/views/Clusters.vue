<script setup>
import { ref, onMounted } from 'vue'
import { readApi, writeApi } from '../api'
import { useClusterStore } from '../stores/cluster'

const { refreshClusters, setCurrentClusterId } = useClusterStore()
const list = ref([])
const error = ref('')
const showForm = ref(false)
const editingId = ref('')
const form = ref({ name: '', description: '', kubeconfig: '' })
const busy = ref(false)

async function load() {
  error.value = ''
  try {
    list.value = await readApi.listClusters()
    await refreshClusters(async () => list.value)
  } catch (e) {
    error.value = e.message
  }
}

function openCreate() {
  editingId.value = ''
  form.value = { name: '', description: '', kubeconfig: '' }
  showForm.value = true
}

function openEdit(c) {
  editingId.value = c.id
  form.value = { name: c.name, description: c.description || '', kubeconfig: '' }
  showForm.value = true
}

async function save() {
  busy.value = true
  error.value = ''
  try {
    if (editingId.value) {
      const payload = {
        name: form.value.name,
        description: form.value.description
      }
      if (form.value.kubeconfig.trim()) payload.kubeconfig = form.value.kubeconfig
      await writeApi.updateCluster(editingId.value, payload)
    } else {
      await writeApi.createCluster(form.value)
    }
    showForm.value = false
    await load()
  } catch (e) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}

async function remove(c) {
  if (!confirm(`删除集群配置「${c.name}」？`)) return
  try {
    await writeApi.deleteCluster(c.id)
    await load()
  } catch (e) {
    error.value = e.message
  }
}

async function test(c) {
  try {
    const r = await readApi.testCluster(c.id)
    alert(`状态: ${r.status}\n类型: ${r.permType}\nServer: ${r.server}\nNamespaces: ${(r.namespaces || []).length}\n${r.lastError || ''}`)
  } catch (e) {
    error.value = e.message
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <div>
        <h1>KubeConfig 管理</h1>
        <p class="page-desc">导入 Rancher 集群管理员或 Project kubeconfig，作为运维与监控凭证。</p>
      </div>
      <button class="btn btn-primary" @click="openCreate">导入 kubeconfig</button>
    </div>
    <p v-if="error" class="error">{{ error }}</p>

    <div v-if="showForm" class="panel">
      <div class="form-row">
        <label>名称</label>
        <input v-model="form.name" type="text" placeholder="例如 prod-cluster" />
      </div>
      <div class="form-row">
        <label>描述</label>
        <input v-model="form.description" type="text" />
      </div>
      <div class="form-row">
        <label>{{ editingId ? 'Kubeconfig YAML（留空则不更新）' : 'Kubeconfig YAML' }}</label>
        <textarea v-model="form.kubeconfig" placeholder="粘贴 kubeconfig 内容"></textarea>
      </div>
      <div class="actions">
        <button class="btn btn-primary" :disabled="busy" @click="save">保存</button>
        <button class="btn" @click="showForm = false">取消</button>
      </div>
    </div>

    <table v-if="list.length" class="table">
      <thead>
        <tr>
          <th>名称</th>
          <th>类型</th>
          <th>Server</th>
          <th>状态</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="c in list" :key="c.id">
          <td>
            <strong>{{ c.name }}</strong>
            <div class="muted">{{ c.description }}</div>
          </td>
          <td><span class="tag" :class="c.permType">{{ c.permType }}</span></td>
          <td class="muted">{{ c.server }}</td>
          <td>
            {{ c.status }}
            <div v-if="c.lastError" class="error">{{ c.lastError }}</div>
          </td>
          <td class="actions">
            <button class="btn btn-sm" @click="setCurrentClusterId(c.id)">选用</button>
            <button class="btn btn-sm" @click="test(c)">测试</button>
            <button class="btn btn-sm" @click="openEdit(c)">编辑</button>
            <button class="btn btn-sm btn-danger" @click="remove(c)">删除</button>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-else class="empty">暂无集群配置，请导入 kubeconfig。</div>
  </div>
</template>
