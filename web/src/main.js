import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './style.css'
import { setUnauthorizedHandler } from './api'

const app = createApp(App)
app.use(createPinia())
app.use(router)

// 任何接口返回 401 都统一跳回登录页
setUnauthorizedHandler(() => {
  if (router.currentRoute.value.name !== 'login') {
    router.push({ name: 'login', query: { redirect: router.currentRoute.value.fullPath } })
  }
})

app.mount('#app')
