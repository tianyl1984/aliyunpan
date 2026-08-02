// SSE 事件流。
// 所有实时消息走 /api/events 这一条多路复用流，避开浏览器每域 6 连接的限制。
// EventSource 只能带 Cookie，所以服务端的认证也走 Cookie。

const listeners = new Map()
let source = null
let reconnectTimer = null
let connectedHandlers = []

export function onEvent(type, fn) {
  if (!listeners.has(type)) listeners.set(type, new Set())
  listeners.get(type).add(fn)
  return () => listeners.get(type)?.delete(fn)
}

export function onReconnect(fn) {
  connectedHandlers.push(fn)
  return () => {
    connectedHandlers = connectedHandlers.filter((f) => f !== fn)
  }
}

function dispatch(type, data) {
  listeners.get(type)?.forEach((fn) => {
    try {
      fn(data)
    } catch (e) {
      console.error('event handler error', type, e)
    }
  })
}

const EVENT_TYPES = [
  'job.added',
  'job.state',
  'job.progress',
  'task.state',
  'task.progress',
  'log',
  'console.output',
  'console.exit',
  'account.changed',
]

export function connectEvents() {
  if (source) return
  source = new EventSource('/api/events', { withCredentials: true })

  EVENT_TYPES.forEach((t) => {
    source.addEventListener(t, (e) => {
      try {
        const payload = JSON.parse(e.data)
        dispatch(t, payload.data)
      } catch {
        /* 忽略解析失败的消息 */
      }
    })
  })

  source.onopen = () => {
    // 重连后需要重新拉一次全量快照，增量事件才对得上
    connectedHandlers.forEach((fn) => fn())
  }

  source.onerror = () => {
    // EventSource 自带重连，但服务重启后可能一直失败，这里做一次兜底重建
    if (source && source.readyState === EventSource.CLOSED) {
      disconnectEvents()
      clearTimeout(reconnectTimer)
      reconnectTimer = setTimeout(connectEvents, 3000)
    }
  }
}

export function disconnectEvents() {
  if (source) {
    source.close()
    source = null
  }
}
