// 统一的接口客户端。
// 所有写操作都会带上 X-Aliyunpan-Client 头，这是服务端 CSRF 防护的一环。

const CLIENT_HEADER = 'X-Aliyunpan-Client'

let onUnauthorized = null
export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn
}

export class ApiError extends Error {
  constructor(code, message) {
    super(message)
    this.code = code
  }
}

// 阿里云盘账号未登录（区别于 webui 自身未认证）
export const CODE_NOT_LOGIN = 1001

async function request(method, url, body, opts = {}) {
  const headers = { [CLIENT_HEADER]: 'webui' }
  let payload
  if (body !== undefined && body !== null) {
    if (body instanceof Blob || body instanceof ArrayBuffer) {
      payload = body
      headers['Content-Type'] = 'application/octet-stream'
    } else {
      payload = JSON.stringify(body)
      headers['Content-Type'] = 'application/json'
    }
  }

  const resp = await fetch(url, {
    method,
    headers,
    body: payload,
    credentials: 'same-origin',
    signal: opts.signal,
  })

  if (resp.status === 401) {
    if (onUnauthorized) onUnauthorized()
    throw new ApiError(401, '未认证或会话已过期')
  }

  let json
  try {
    json = await resp.json()
  } catch {
    throw new ApiError(resp.status, `服务端返回异常 (${resp.status})`)
  }
  if (json.code !== 0) {
    throw new ApiError(json.code, json.message || '请求失败')
  }
  return json.data
}

const get = (url, opts) => request('GET', url, null, opts)
const post = (url, body, opts) => request('POST', url, body ?? {}, opts)
const put = (url, body, opts) => request('PUT', url, body ?? {}, opts)
const del = (url, opts) => request('DELETE', url, null, opts)

function qs(params) {
  const sp = new URLSearchParams()
  Object.entries(params || {}).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') sp.set(k, v)
  })
  const s = sp.toString()
  return s ? `?${s}` : ''
}

export const api = {
  // 认证
  authStatus: () => get('/api/auth/status'),
  authLogin: (password) => post('/api/auth/login', { password }),
  authLogout: () => post('/api/auth/logout'),

  // 账号
  accountCurrent: () => get('/api/account/current'),
  accountList: () => get('/api/account/list'),
  accountDrives: () => get('/api/account/drives'),
  accountQuota: () => get('/api/account/quota'),
  accountSwitch: (userId) => post('/api/account/switch', { userId }),
  driveSwitch: (driveId) => post('/api/account/drive/switch', { driveId }),
  accountDelete: (userId) => del(`/api/account/${encodeURIComponent(userId)}`),
  oauthStart: () => post('/api/account/oauth/start'),
  oauthPoll: (ticketId) => get(`/api/account/oauth/poll${qs({ ticketId })}`),

  // 配置
  configGet: () => get('/api/config'),
  configUpdate: (patch) => put('/api/config', patch),
  systemInfo: () => get('/api/system/info'),

  // 文件
  fileList: (params) => get(`/api/files${qs(params)}`),
  fileInfo: (params) => get(`/api/files/info${qs(params)}`),
  fileSearch: (params) => get(`/api/files/search${qs(params)}`),
  mkdir: (driveId, path) => post('/api/files/mkdir', { driveId, path }),
  remove: (driveId, paths) => post('/api/files/delete', { driveId, paths }),
  copy: (driveId, srcPaths, dstPath) => post('/api/files/copy', { driveId, srcPaths, dstPath }),
  move: (driveId, srcPaths, dstPath) => post('/api/files/move', { driveId, srcPaths, dstPath }),
  rename: (driveId, path, newName) => post('/api/files/rename', { driveId, path, newName }),
  contentUrl: (params) => `/api/files/content${qs(params)}`,
  previewUrl: (params) => `/api/files/preview${qs(params)}`,
  thumbnailUrl: (params) => `/api/files/thumbnail${qs(params)}`,

  // 服务器本地文件
  localRoots: () => get('/api/local/roots'),
  localList: (params) => get(`/api/local/ls${qs(params)}`),

  // 传输
  jobs: (params) => get(`/api/transfer/jobs${qs(params)}`),
  job: (id) => get(`/api/transfer/jobs/${encodeURIComponent(id)}`),
  download: (payload) => post('/api/transfer/download', payload),
  upload: (payload) => post('/api/transfer/upload', payload),
  jobPause: (id) => post(`/api/transfer/jobs/${encodeURIComponent(id)}/pause`),
  jobResume: (id) => post(`/api/transfer/jobs/${encodeURIComponent(id)}/resume`),
  jobCancel: (id) => post(`/api/transfer/jobs/${encodeURIComponent(id)}/cancel`),
  jobRetry: (id) => post(`/api/transfer/jobs/${encodeURIComponent(id)}/retry`),
  jobDelete: (id) => del(`/api/transfer/jobs/${encodeURIComponent(id)}`),
  transferClear: () => post('/api/transfer/clear'),

  // 浏览器直传
  uploadSessionCreate: (payload) => post('/api/upload/session', payload),
  uploadSessionChunk: (id, offset, blob, opts) =>
    request('PUT', `/api/upload/session/${encodeURIComponent(id)}/chunk${qs({ offset })}`, blob, opts),
  uploadSessionComplete: (id) => post(`/api/upload/session/${encodeURIComponent(id)}/complete`),
  uploadSessionDelete: (id) => del(`/api/upload/session/${encodeURIComponent(id)}`),

  // 控制台
  consoleCommands: () => get('/api/console/commands'),
  consoleExec: (argv, timeoutSec) => post('/api/console/exec', { argv, timeoutSec: timeoutSec || 0 }),
}
