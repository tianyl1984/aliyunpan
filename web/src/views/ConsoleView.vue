<script setup>
// 兜底控制台：把任意 CLI 命令喂给服务端的 cmder.App() 并实时回显 stdout。
// 没有做图形界面的命令（tree / share / album / recycle / save / xcp / tool …）都能在这里用。
import { ref, onMounted, onUnmounted, nextTick, computed } from 'vue'
import { api } from '../api'
import { onEvent } from '../api/events'
import { useToastStore } from '../stores'

const toast = useToastStore()
const commands = ref([])
const allowShell = ref(false)
const input = ref('')
const output = ref('')
const running = ref(false)
const outEl = ref(null)
const history = ref([])
const histIdx = ref(-1)
let currentSession = null
const offs = []

const denied = computed(() => commands.value.filter((c) => !c.allowed))
const allowed = computed(() => commands.value.filter((c) => c.allowed))

onMounted(async () => {
  try {
    const d = await api.consoleCommands()
    commands.value = d.commands || []
    allowShell.value = d.allowShell
  } catch (e) {
    toast.error(e.message)
  }

  offs.push(
    onEvent('console.output', (d) => {
      if (d.sessionId !== currentSession) return
      output.value += d.chunk
      scroll()
    }),
  )
  offs.push(
    onEvent('console.exit', (d) => {
      if (d.sessionId !== currentSession) return
      if (d.message) output.value += `\n[错误] ${d.message}\n`
      output.value += `\n[结束] 退出码 ${d.code}，耗时 ${d.durationMs}ms\n\n`
      running.value = false
      currentSession = null
      scroll()
    }),
  )
})

onUnmounted(() => offs.forEach((f) => f()))

function scroll() {
  nextTick(() => {
    if (outEl.value) outEl.value.scrollTop = outEl.value.scrollHeight
  })
}

// 复用与 CLI 相同的语义：按空格切分，支持双引号包裹带空格的参数
function parseArgv(line) {
  const out = []
  let cur = ''
  let quote = null
  for (const ch of line.trim()) {
    if (quote) {
      if (ch === quote) quote = null
      else cur += ch
    } else if (ch === '"' || ch === "'") {
      quote = ch
    } else if (ch === ' ') {
      if (cur) { out.push(cur); cur = '' }
    } else {
      cur += ch
    }
  }
  if (cur) out.push(cur)
  return out
}

async function run() {
  const line = input.value.trim()
  if (!line || running.value) return
  const argv = parseArgv(line)
  if (!argv.length) return

  history.value.unshift(line)
  histIdx.value = -1
  output.value += `$ ${line}\n`
  input.value = ''
  running.value = true
  scroll()

  try {
    const d = await api.consoleExec(argv)
    currentSession = d.sessionId
  } catch (e) {
    output.value += `[错误] ${e.message}\n\n`
    running.value = false
    scroll()
  }
}

function historyPrev() {
  if (histIdx.value + 1 < history.value.length) {
    histIdx.value++
    input.value = history.value[histIdx.value]
  }
}
function historyNext() {
  if (histIdx.value > 0) {
    histIdx.value--
    input.value = history.value[histIdx.value]
  } else {
    histIdx.value = -1
    input.value = ''
  }
}
function fill(name) {
  input.value = name + ' '
}
</script>

<template>
  <div>
    <h2 class="page-title">控制台</h2>

    <div class="card">
      <div class="card-head">
        命令输出
        <div class="spacer"></div>
        <button class="btn sm" @click="output = ''">清空</button>
      </div>
      <div class="card-body">
        <pre ref="outEl" class="console-out">{{ output || '在下方输入命令，例如：tree / 或 share list' }}</pre>
        <div class="row" style="margin-top: 10px">
          <span class="mono">$</span>
          <input
            v-model="input"
            type="text"
            class="mono"
            placeholder="输入命令，回车执行"
            :disabled="running"
            style="flex: 1"
            @keyup.enter="run"
            @keydown.up.prevent="historyPrev"
            @keydown.down.prevent="historyNext"
          />
          <button class="btn primary" :disabled="running || !input.trim()" @click="run">
            {{ running ? '执行中…' : '执行' }}
          </button>
        </div>
        <p class="small muted" style="margin-bottom: 0">
          同一时刻只能执行一条命令（服务端需要全局重定向标准输出）。方向键 ↑ ↓ 可翻历史。
        </p>
      </div>
    </div>

    <div class="card">
      <div class="card-head">可用命令 ({{ allowed.length }})</div>
      <div class="card-body">
        <div class="row">
          <button v-for="c in allowed" :key="c.name" class="btn sm" :title="c.usage" @click="fill(c.name)">
            {{ c.name }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="denied.length" class="card">
      <div class="card-head">已禁用命令 ({{ denied.length }})</div>
      <table>
        <tbody>
          <tr v-for="c in denied" :key="c.name">
            <td style="width: 110px" class="mono">{{ c.name }}</td>
            <td class="small muted">{{ c.reason }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
