<script setup>
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore, useToastStore } from '../stores'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const toast = useToastStore()

const password = ref('')
const busy = ref(false)
const input = ref(null)

onMounted(() => input.value?.focus())

async function submit() {
  if (busy.value) return
  busy.value = true
  try {
    await auth.login(password.value)
    router.push(route.query.redirect || '/files')
  } catch (e) {
    toast.error(e.message)
    password.value = ''
    input.value?.focus()
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <div class="login-box card">
      <div class="card-head">☁️ aliyunpan Web 管理界面</div>
      <div class="card-body">
        <p class="muted small" style="margin-top: 0">
          输入启动服务时打印的访问 token，或 <code>--password</code> 指定的口令。
        </p>
        <form @submit.prevent="submit">
          <input
            ref="input"
            v-model="password"
            type="password"
            placeholder="访问口令 / token"
            autocomplete="current-password"
          />
          <button class="btn primary" style="width: 100%; margin-top: 12px" :disabled="busy || !password">
            {{ busy ? '登录中…' : '登录' }}
          </button>
        </form>
      </div>
    </div>
  </div>
</template>
