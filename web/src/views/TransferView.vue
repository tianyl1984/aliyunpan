<script setup>
import { ref, onMounted } from 'vue'
import { api } from '../api'
import { useTransferStore, useToastStore } from '../stores'
import { formatSize, formatSpeed, formatTime, percent } from '../utils'

const transfer = useTransferStore()
const toast = useToastStore()
const expanded = ref(new Set())
const tick = ref(0)

onMounted(() => {
  transfer.refresh().catch((e) => toast.error(e.message))
})

async function toggle(job) {
  const s = new Set(expanded.value)
  if (s.has(job.id)) {
    s.delete(job.id)
  } else {
    s.add(job.id)
    try {
      await transfer.loadDetail(job.id)
    } catch (e) {
      toast.error(e.message)
    }
  }
  expanded.value = s
  tick.value++
}

async function act(fn, id, okMsg) {
  try {
    await fn(id)
    if (okMsg) toast.success(okMsg)
    await transfer.refresh()
  } catch (e) {
    toast.error(e.message)
  }
}

async function clearFinished() {
  try {
    const d = await api.transferClear()
    toast.success(`已清理 ${d.removed} 条记录`)
    await transfer.refresh()
  } catch (e) {
    toast.error(e.message)
  }
}

const STATE_TEXT = {
  queued: '排队中', running: '进行中', paused: '已暂停',
  completed: '已完成', failed: '失败', canceled: '已取消',
}
const TASK_STATE_TEXT = {
  queued: '排队', running: '传输中', success: '完成',
  failed: '失败', canceled: '取消', retrying: '重试中',
}
</script>

<template>
  <div>
    <h2 class="page-title">传输</h2>

    <div class="card">
      <div class="card-head">
        <span>进行中 {{ transfer.active.length }}</span>
        <div class="spacer"></div>
        <button class="btn sm" @click="transfer.refresh()">↻ 刷新</button>
        <button class="btn sm" @click="clearFinished">清理已结束</button>
      </div>

      <div v-if="!transfer.jobs.length" class="empty">暂无传输任务</div>

      <table v-else :key="tick">
        <thead>
          <tr>
            <th style="width: 26px"></th>
            <th>任务</th>
            <th style="width: 82px">类型</th>
            <th style="width: 80px">状态</th>
            <th style="width: 190px">进度</th>
            <th style="width: 96px">速度</th>
            <th style="width: 180px"></th>
          </tr>
        </thead>
        <tbody>
          <template v-for="j in transfer.jobs" :key="j.id">
            <tr>
              <td>
                <button class="btn sm" style="padding: 0 5px" @click="toggle(j)">
                  {{ expanded.has(j.id) ? '▾' : '▸' }}
                </button>
              </td>
              <td class="truncate" style="max-width: 1px">
                <div class="truncate">{{ j.title }}</div>
                <div class="small muted truncate">
                  {{ j.type === 'download' ? `→ ${j.saveTo}` : `→ ${j.panDir}` }}
                  · {{ formatTime(j.createdAt) }}
                </div>
              </td>
              <td class="small">{{ j.type === 'download' ? '下载' : '上传' }}</td>
              <td><span class="tag" :class="j.state">{{ STATE_TEXT[j.state] || j.state }}</span></td>
              <td>
                <div class="progress"><i :style="{ width: percent(j.bytesDone, j.bytesTotal) + '%' }" /></div>
                <div class="small muted">
                  {{ j.filesDone }}/{{ j.filesTotal }} 文件
                  <span v-if="j.filesFailed"> · 失败 {{ j.filesFailed }}</span>
                  · {{ formatSize(j.bytesDone) }}/{{ formatSize(j.bytesTotal) }}
                </div>
              </td>
              <td class="small">{{ formatSpeed(j.speed) }}</td>
              <td>
                <button v-if="j.state === 'running' || j.state === 'queued'" class="btn sm"
                  @click="act(api.jobPause, j.id)">暂停</button>
                <button v-if="j.state === 'paused'" class="btn sm"
                  @click="act(api.jobResume, j.id)">继续</button>
                <button v-if="!['completed','failed','canceled'].includes(j.state)" class="btn sm danger"
                  @click="act(api.jobCancel, j.id)">取消</button>
                <button v-if="['failed','canceled'].includes(j.state)" class="btn sm"
                  @click="act(api.jobRetry, j.id, '已重新提交')">重试</button>
                <button v-if="['completed','failed','canceled'].includes(j.state)" class="btn sm"
                  @click="act(api.jobDelete, j.id)">移除</button>
              </td>
            </tr>
            <tr v-if="j.message">
              <td></td>
              <td colspan="6" class="small muted">{{ j.message }}</td>
            </tr>
            <tr v-if="expanded.has(j.id)">
              <td></td>
              <td colspan="6" style="padding: 0 10px 10px">
                <table>
                  <tbody>
                    <tr v-for="t in j.tasks || []" :key="t.id">
                      <td class="truncate small" style="max-width: 1px">{{ t.path }}</td>
                      <td style="width: 80px">
                        <span class="tag">{{ TASK_STATE_TEXT[t.state] || t.state }}</span>
                      </td>
                      <td style="width: 190px">
                        <div class="progress"><i :style="{ width: percent(t.done, t.size) + '%' }" /></div>
                        <div class="small muted">{{ formatSize(t.done) }}/{{ formatSize(t.size) }}</div>
                      </td>
                      <td class="small" style="width: 96px">{{ formatSpeed(t.speed) }}</td>
                      <td class="small muted truncate" style="max-width: 1px">{{ t.message }}</td>
                    </tr>
                    <tr v-if="!(j.tasks || []).length">
                      <td class="small muted">暂无文件明细</td>
                    </tr>
                  </tbody>
                </table>
                <p v-if="j.type === 'upload'" class="small muted" style="margin: 8px 0 0">
                  说明：上传没有本地临时文件，服务端拿不到单文件字节进度，这里只展示状态与整体速率。
                </p>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>

    <p class="small muted">
      暂停与取消在文件边界生效：已经在传输的文件会先跑完，队列里未开始的文件立即停止。
      断点信息会保留，继续或重试时自动续传。
    </p>
  </div>
</template>
