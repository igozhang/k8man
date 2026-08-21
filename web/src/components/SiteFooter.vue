<script setup>
import { useSiteUI } from '../composables/useSiteUI'

const { ui } = useSiteUI()
</script>

<template>
  <footer class="footer">
    <div class="shell footer-inner">
      k8man · 通过导入 kubeconfig 管理与监控 K8s 集群 · v{{ ui.version }}
    </div>
  </footer>
</template>
