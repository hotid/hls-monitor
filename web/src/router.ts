import { createRouter, createWebHistory } from 'vue-router'
import { basePath } from './api'
import { useSession } from './stores/session'

export const router = createRouter({
  history: createWebHistory(basePath + '/'),
  routes: [
    { path: '/login', name: 'login', component: () => import('./views/LoginView.vue'), meta: { public: true } },
    { path: '/', name: 'dashboard', component: () => import('./views/DashboardView.vue') },
    { path: '/streams', name: 'streams', component: () => import('./views/StreamsView.vue') },
    { path: '/streams/:id(\\d+)', name: 'stream', component: () => import('./views/StreamView.vue'), props: (r) => ({ id: Number(r.params.id) }) },
    { path: '/streams/:id(\\d+)/diagnose', name: 'diagnose', component: () => import('./views/DiagnoseView.vue'), props: (r) => ({ id: Number(r.params.id) }) },
    { path: '/streams/:id(\\d+)/watch', name: 'watch', component: () => import('./views/WatchView.vue'), props: (r) => ({ id: Number(r.params.id) }) },
    { path: '/servers/:host/:ip?', name: 'server', component: () => import('./views/HostView.vue'), props: true },
    { path: '/incidents', name: 'incidents', component: () => import('./views/IncidentsView.vue') },
    { path: '/agents', name: 'agents', component: () => import('./views/AgentsView.vue') },
    { path: '/settings', name: 'settings', component: () => import('./views/AccessView.vue'), meta: { admin: true } },
    // The page used to be called Access.
    { path: '/access', redirect: '/settings' },
    { path: '/:rest(.*)*', redirect: '/' },
  ],
  scrollBehavior: (_to, _from, saved) => saved ?? { top: 0 },
})

router.beforeEach(async (to) => {
  const session = useSession()
  if (!session.ready) await session.restore()
  if (!session.user && !to.meta.public) return { name: 'login', query: to.fullPath !== '/' ? { next: to.fullPath } : {} }
  if (session.user && to.name === 'login') return { name: 'dashboard' }
  if (to.meta.admin && !session.isAdmin) return { name: 'dashboard' }
  return true
})
