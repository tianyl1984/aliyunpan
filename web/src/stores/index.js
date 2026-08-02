import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api } from '../api'
import { onEvent, onReconnect } from '../api/events'

export const useAuthStore = defineStore('auth', () => {
  const authenticated = ref(false)
  const checked = ref(false)

  async function check() {
    try {
      const d = await api.authStatus()
      authenticated.value = !!d.authenticated
    } catch {
      authenticated.value = false
    } finally {
      checked.value = true
    }
    return authenticated.value
  }

  async function login(password) {
    await api.authLogin(password)
    authenticated.value = true
  }

  async function logout() {
    try {
      await api.authLogout()
    } finally {
      authenticated.value = false
    }
  }

  return { authenticated, checked, check, login, logout }
})

export const useAccountStore = defineStore('account', () => {
  const loggedIn = ref(false)
  const account = ref(null)
  const accounts = ref([])
  const quota = ref(null)
  const loading = ref(false)

  const drives = computed(() => account.value?.drives || [])
  const activeDriveId = computed(() => account.value?.activeDriveId || '')
  const activeDrive = computed(() => drives.value.find((d) => d.driveId === activeDriveId.value) || null)

  async function refresh() {
    loading.value = true
    try {
      const d = await api.accountCurrent()
      loggedIn.value = !!d.loggedIn
      account.value = d.account || null
      if (loggedIn.value) {
        const l = await api.accountList()
        accounts.value = l.accounts || []
      } else {
        accounts.value = []
        quota.value = null
      }
    } finally {
      loading.value = false
    }
  }

  async function refreshQuota() {
    if (!loggedIn.value) return
    try {
      quota.value = await api.accountQuota()
    } catch {
      quota.value = null
    }
  }

  async function switchDrive(driveId) {
    account.value = await api.driveSwitch(driveId)
  }

  async function switchAccount(userId) {
    account.value = await api.accountSwitch(userId)
    await refresh()
  }

  onEvent('account.changed', () => {
    refresh()
  })

  return {
    loggedIn, account, accounts, quota, loading,
    drives, activeDriveId, activeDrive,
    refresh, refreshQuota, switchDrive, switchAccount,
  }
})

export const useTransferStore = defineStore('transfer', () => {
  const jobs = ref([])
  const loaded = ref(false)

  const active = computed(() =>
    jobs.value.filter((j) => ['queued', 'running', 'paused'].includes(j.state)))
  const finished = computed(() =>
    jobs.value.filter((j) => ['completed', 'failed', 'canceled'].includes(j.state)))
  const activeCount = computed(() => active.value.length)

  function upsert(job) {
    if (!job || !job.id) return
    const i = jobs.value.findIndex((j) => j.id === job.id)
    if (i >= 0) {
      // 保留已展开的 tasks 明细，增量事件不带它
      const prev = jobs.value[i]
      jobs.value[i] = { ...prev, ...job, tasks: job.tasks || prev.tasks }
    } else {
      jobs.value.unshift(job)
    }
  }

  async function refresh() {
    const d = await api.jobs()
    jobs.value = d.jobs || []
    loaded.value = true
  }

  async function loadDetail(id) {
    const d = await api.job(id)
    upsert(d)
    return d
  }

  function patchTask(jobId, task) {
    const job = jobs.value.find((j) => j.id === jobId)
    if (!job || !job.tasks) return
    const i = job.tasks.findIndex((t) => t.id === task.id)
    if (i >= 0) job.tasks[i] = { ...job.tasks[i], ...task }
    else job.tasks.push(task)
  }

  onEvent('job.added', upsert)
  onEvent('job.state', upsert)
  onEvent('job.progress', upsert)
  onEvent('task.state', (d) => patchTask(d.jobId, d.task))
  onEvent('task.progress', (d) => patchTask(d.jobId, d.task))
  onReconnect(() => { refresh().catch(() => {}) })

  return { jobs, loaded, active, finished, activeCount, refresh, loadDetail, upsert }
})

export const useToastStore = defineStore('toast', () => {
  const items = ref([])
  let seq = 0

  function push(text, kind = 'info', ttl = 4000) {
    const id = ++seq
    items.value.push({ id, text, kind })
    setTimeout(() => {
      items.value = items.value.filter((t) => t.id !== id)
    }, ttl)
  }

  const success = (t) => push(t, 'success')
  const error = (t) => push(t, 'error', 6000)
  const info = (t) => push(t, 'info')

  return { items, push, success, error, info }
})
