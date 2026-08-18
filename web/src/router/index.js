import { createRouter, createWebHistory } from 'vue-router'
import Overview from '../views/Overview.vue'
import Clusters from '../views/Clusters.vue'
import Workloads from '../views/Workloads.vue'
import Monitor from '../views/Monitor.vue'
import Docs from '../views/Docs.vue'
import DocDetail from '../views/DocDetail.vue'
import DocEdit from '../views/DocEdit.vue'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'overview', component: Overview },
    { path: '/clusters', name: 'clusters', component: Clusters },
    { path: '/workloads', name: 'workloads', component: Workloads },
    { path: '/nodes', redirect: '/monitor' },
    { path: '/monitor', name: 'monitor', component: Monitor },
    { path: '/docs', name: 'docs', component: Docs },
    { path: '/docs/edit/:id?', name: 'doc-edit', component: DocEdit },
    { path: '/docs/:id', name: 'doc-detail', component: DocDetail }
  ],
  scrollBehavior() {
    return { top: 0 }
  }
})

export default router
