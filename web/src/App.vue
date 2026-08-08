<script setup>
import { computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore, useAccountStore, useTransferStore, useToastStore } from './stores'
import { connectEvents, disconnectEvents } from './api/events'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const account = useAccountStore()
const transfer = useTransferStore()
const toast = useToastStore()

const showShell = computed(() => route.name !== 'login')

watch(
  () => auth.authenticated,
  (ok) => {
    if (ok) {
      connectEvents()
      account.refresh().catch(() => {})
      transfer.refresh().catch(() => {})
    } else {
      disconnectEvents()
    }
  },
  { immediate: true },
)

onMounted(() => {
  if (auth.authenticated) connectEvents()
})

async function doLogout() {
  await auth.logout()
  router.push({ name: 'login' })
}
</script>

<template>
  <div v-if="showShell" class="layout">
    <aside class="sidebar">
      <div class="brand">☁️ <span>aliyunpan</span></div>
      <nav class="nav">
        <RouterLink to="/files">📁 <span class="label">文件</span></RouterLink>
        <RouterLink to="/transfer">
          ⇅ <span class="label">传输</span>
          <span v-if="transfer.activeCount" class="badge">{{ transfer.activeCount }}</span>
        </RouterLink>
        <RouterLink to="/account">👤 <span class="label">账号</span></RouterLink>
        <RouterLink to="/settings">⚙️ <span class="label">设置</span></RouterLink>
        <RouterLink to="/console">▶ <span class="label">控制台</span></RouterLink>
      </nav>
      <div class="sidebar-foot">
        <div v-if="account.loggedIn" class="truncate small">{{ account.account?.nickname }}</div>
        <div v-else class="small">未登录云盘</div>
        <div v-if="auth.user" class="truncate small muted">🔑 {{ auth.user }}</div>
        <button class="btn sm" style="margin-top: 8px; width: 100%" @click="doLogout">退出 Web</button>
      </div>
    </aside>

    <main class="main">
      <div class="content">
        <RouterView />
      </div>
    </main>
  </div>

  <RouterView v-else />

  <div class="toasts">
    <div v-for="t in toast.items" :key="t.id" class="toast" :class="t.kind">{{ t.text }}</div>
  </div>
</template>
