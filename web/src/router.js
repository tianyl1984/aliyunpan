import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from './stores'

const routes = [
  { path: '/', redirect: '/files' },
  { path: '/login', name: 'login', component: () => import('./views/LoginView.vue'), meta: { public: true } },
  { path: '/files', name: 'files', component: () => import('./views/FilesView.vue') },
  { path: '/transfer', name: 'transfer', component: () => import('./views/TransferView.vue') },
  { path: '/account', name: 'account', component: () => import('./views/AccountView.vue') },
  { path: '/settings', name: 'settings', component: () => import('./views/SettingsView.vue') },
  { path: '/console', name: 'console', component: () => import('./views/ConsoleView.vue') },
  { path: '/:pathMatch(.*)*', redirect: '/files' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!auth.checked) await auth.check()

  if (to.meta.public) {
    if (to.name === 'login' && auth.authenticated) return { path: '/files' }
    return true
  }
  if (!auth.authenticated) {
    return { name: 'login', query: to.fullPath !== '/' ? { redirect: to.fullPath } : undefined }
  }
  return true
})

export default router
