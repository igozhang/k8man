import { createRouter, createWebHistory } from 'vue-router'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/',
      name: 'overview',
      component: () => import('../views/Overview.vue')
    },
    {
      path: '/clusters',
      name: 'clusters',
      component: () => import('../views/Clusters.vue')
    },
    {
      path: '/workloads',
      name: 'workloads',
      component: () => import('../views/Workloads.vue')
    },
    { path: '/nodes', redirect: '/monitor' },
    {
      path: '/monitor',
      name: 'monitor',
      component: () => import('../views/Monitor.vue')
    },
    {
      path: '/diagnose',
      name: 'diagnose',
      component: () => import('../views/Diagnose.vue')
    },
    {
      path: '/docs',
      name: 'docs',
      component: () => import('../views/Docs.vue')
    },
    {
      path: '/docs/edit/:id?',
      name: 'doc-edit',
      component: () => import('../views/DocEdit.vue')
    },
    {
      path: '/docs/:id',
      name: 'doc-detail',
      component: () => import('../views/DocDetail.vue')
    }
  ],
  scrollBehavior() {
    return { top: 0 }
  }
})

export default router
