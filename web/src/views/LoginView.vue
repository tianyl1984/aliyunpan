<script setup>
import { computed, ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore, useToastStore } from '../stores'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const toast = useToastStore()

const password = ref('')
const busy = ref(false)
const input = ref(null)

const redirect = computed(() => route.query.redirect || '/files')

onMounted(() => {
  // 第三方登录失败时服务端会 302 回这里并把原因带在 query 上
  const err = route.query.error
  if (err) {
    toast.error(String(err))
    router.replace({ name: 'login', query: { ...route.query, error: undefined } })
  }
  input.value?.focus()
})

async function submit() {
  if (busy.value) return
  busy.value = true
  try {
    await auth.login(password.value)
    router.push(redirect.value)
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

        <template v-if="auth.oauth">
          <div class="login-sep"><span>或</span></div>
          <button class="btn" style="width: 100%" @click="auth.loginExternal(redirect)">
            使用 {{ auth.oauth.provider }} 登录
          </button>
          <p class="muted small" style="margin-bottom: 0">
            将跳转到认证服务完成授权，仅白名单用户可登录。
          </p>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.login-sep {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 18px 0 12px;
  color: var(--muted);
  font-size: 12px;
}
.login-sep::before,
.login-sep::after {
  content: '';
  flex: 1;
  height: 1px;
  background: var(--border);
}
</style>
