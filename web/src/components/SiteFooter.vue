<script setup>
import { useRouter } from 'vue-router'
import { useSiteUI } from '../composables/useSiteUI'
import { canWrite } from '../stores/opsAuth'

const { ui } = useSiteUI()
const router = useRouter()

function onKubeconfigDblclick() {
  if (!canWrite.value) return
  if (router.currentRoute.value.path === '/clusters') return
  router.push('/clusters')
}
</script>

<template>
  <footer class="footer">
    <div class="shell footer-inner">
      k8man · 通过导入 <span
        class="footer-kubeconfig"
        aria-hidden="true"
        @dblclick.prevent="onKubeconfigDblclick"
      >kubeconfig</span> 管理与监控 K8s 集群 · v{{ ui.version }}
    </div>
  </footer>
</template>
