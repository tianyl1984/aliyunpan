<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { useAccountStore, useToastStore } from '../stores'
import { formatSize } from '../utils'

const account = useAccountStore()
const toast = useToastStore()

const login = ref(null) // { ticketId, authorizeUrl }
const polling = ref(false)
let timer = null

onMounted(async () => {
  await account.refresh().catch((e) => toast.error(e.message))
  account.refreshQuota()
})
onUnmounted(() => stopPolling())

function stopPolling() {
  clearInterval(timer)
  timer = null
  polling.value = false
}

async function startLogin() {
  try {
    login.value = await api.oauthStart()
    window.open(login.value.authorizeUrl, '_blank', 'noopener')
    polling.value = true
    // CLI 的 login 命令在这里阻塞等用户按 Enter；Web 端换成轮询
    timer = setInterval(poll, 3000)
    setTimeout(() => { if (polling.value) { stopPolling(); toast.error('登录超时，请重试') } }, 5 * 60 * 1000)
  } catch (e) {
    toast.error(e.message)
  }
}

async function poll() {
  if (!login.value) return
  try {
    const d = await api.oauthPoll(login.value.ticketId)
    if (d.status === 'ok') {
      stopPolling()
      login.value = null
      toast.success('登录成功')
      await account.refresh()
      account.refreshQuota()
    }
  } catch (e) {
    stopPolling()
    toast.error(e.message)
  }
}

async function switchTo(userId) {
  try {
    await account.switchAccount(userId)
    account.refreshQuota()
    toast.success('已切换账号')
  } catch (e) {
    toast.error(e.message)
  }
}

async function removeAccount(a) {
  if (!confirm(`确认退出账号 ${a.nickname || a.userId}？`)) return
  try {
    await api.accountDelete(a.userId)
    toast.success('已退出')
    await account.refresh()
  } catch (e) {
    toast.error(e.message)
  }
}

async function switchDrive(id) {
  try {
    await account.switchDrive(id)
    toast.success('已切换网盘')
  } catch (e) {
    toast.error(e.message)
  }
}
</script>

<template>
  <div>
    <h2 class="page-title">账号</h2>

    <div class="card">
      <div class="card-head">
        当前账号
        <div class="spacer"></div>
        <button class="btn sm primary" :disabled="polling" @click="startLogin">
          {{ polling ? '等待授权中…' : '＋ 扫码登录新账号' }}
        </button>
      </div>
      <div class="card-body">
        <div v-if="login" class="card" style="margin-bottom: 12px">
          <div class="card-body">
            <p style="margin-top: 0">
              已在新窗口打开授权页面。需要完成<b>授权</b>和<b>扫码</b>两步登录，完成后本页会自动刷新。
            </p>
            <p class="small">
              没有弹出？<a :href="login.authorizeUrl" target="_blank" rel="noopener">点此手动打开</a>
            </p>
            <button class="btn sm" @click="stopPolling(); login = null">取消</button>
          </div>
        </div>

        <div v-if="!account.loggedIn" class="muted">尚未登录任何阿里云盘账号。</div>
        <template v-else>
          <div class="row">
            <b>{{ account.account.nickname }}</b>
            <span class="muted small mono">{{ account.account.userId }}</span>
          </div>
          <div v-if="account.quota" style="margin-top: 12px; max-width: 480px">
            <div class="progress">
              <i :style="{ width: Math.min(100, account.quota.usedRatio) + '%' }" />
            </div>
            <div class="small muted" style="margin-top: 4px">
              已用 {{ formatSize(account.quota.usedSize) }} /
              {{ formatSize(account.quota.totalSize) }}
              ({{ account.quota.usedRatio.toFixed(1) }}%)
              <span v-if="account.quota.thirdPartyVip"> · 三方权益包 {{ account.quota.thirdPartyVipExpire }}</span>
            </div>
          </div>
        </template>
      </div>
    </div>

    <div v-if="account.loggedIn" class="card">
      <div class="card-head">网盘</div>
      <table>
        <tbody>
          <tr v-for="d in account.drives" :key="d.driveId">
            <td>{{ d.driveName }}</td>
            <td class="small muted mono">{{ d.driveTag }}</td>
            <td style="width: 120px">
              <span v-if="d.active" class="tag completed">当前</span>
              <button v-else class="btn sm" @click="switchDrive(d.driveId)">切换</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="account.accounts.length" class="card">
      <div class="card-head">已登录账号 ({{ account.accounts.length }})</div>
      <table>
        <tbody>
          <tr v-for="a in account.accounts" :key="a.userId">
            <td>{{ a.nickname || '(未命名)' }}</td>
            <td class="small muted mono hide-sm">{{ a.userId }}</td>
            <td style="width: 170px">
              <span v-if="a.active" class="tag completed">当前</span>
              <button v-else class="btn sm" @click="switchTo(a.userId)">切换</button>
              <button class="btn sm danger" @click="removeAccount(a)">退出</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
