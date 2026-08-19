<script setup>
import { RouterLink } from 'vue-router'
import { useSiteUI } from '../composables/useSiteUI'

const { ui, iconSrc, bannerStyle } = useSiteUI()
</script>

<template>
  <header class="site-head" :class="{ 'has-banner': ui.hasBanner }" :style="bannerStyle">
    <div class="shell site-head-inner">
      <!-- 标题靠左，导航靠右，与正文同宽 -->
      <div class="head-mid">
        <div class="brand-block">
          <RouterLink to="/" class="brand-home brand-home-icon">
            <img class="brand-icon" :src="iconSrc" width="36" height="36" alt="" />
          </RouterLink>
          <div class="brand-copy">
            <RouterLink to="/" class="brand brand-home">{{ ui.title }}</RouterLink>
            <p v-if="ui.tagline" class="site-tagline">{{ ui.tagline }}</p>
          </div>
        </div>
        <nav class="nav">
          <RouterLink to="/">概览</RouterLink>
          <RouterLink to="/clusters">KubeConfig</RouterLink>
          <RouterLink to="/workloads">工作负载</RouterLink>
          <RouterLink to="/monitor">监控</RouterLink>
          <RouterLink to="/diagnose">诊断</RouterLink>
          <RouterLink to="/docs">运维文档</RouterLink>
        </nav>
      </div>
    </div>
  </header>
</template>
