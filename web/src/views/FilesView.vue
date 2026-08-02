<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, CODE_NOT_LOGIN } from '../api'
import { useAccountStore, useToastStore, useTransferStore } from '../stores'
import { breadcrumbs, formatSize, fileIcon, isPreviewable, joinPath, parentPath } from '../utils'
import LocalPathPicker from '../components/LocalPathPicker.vue'
import PanDirPicker from '../components/PanDirPicker.vue'
import FilePreview from '../components/FilePreview.vue'

const route = useRoute()
const router = useRouter()
const account = useAccountStore()
const toast = useToastStore()
const transfer = useTransferStore()

// Web 端完全无状态：当前路径由前端持有并同步到 URL，
// 服务端从不读写 PanUser.Workdir，因此多标签页互不干扰，也不会污染 CLI 的 pwd。
const cwd = ref(route.query.path || '/')
const driveId = ref(route.query.drive || '')
const files = ref([])
const target = ref(null)
const nextMarker = ref('')
const loading = ref(false)
const notLogin = ref(false)
const selected = ref(new Set())
const selTick = ref(0)
const orderBy = ref('name')
const order = ref('asc')
const keyword = ref('')
const searching = ref(false)

const preview = ref(null)
const showLocalPicker = ref(false)
const showUploadPicker = ref(false)
const showPanPicker = ref(false)
const panPickerMode = ref('copy')
const uploading = ref(null)

const selectedFiles = computed(() => {
  selTick.value
  return files.value.filter((f) => selected.value.has(f.path))
})
const allChecked = computed(() =>
  files.value.length > 0 && selectedFiles.value.length === files.value.length)

watch([cwd, driveId], () => {
  router.replace({ query: { ...route.query, path: cwd.value, drive: driveId.value || undefined } })
})

watch(() => account.activeDriveId, (v) => {
  if (!driveId.value && v) driveId.value = v
})

onMounted(async () => {
  if (!account.loggedIn) await account.refresh().catch(() => {})
  if (!driveId.value) driveId.value = account.activeDriveId || ''
  load()
})

async function load(path = cwd.value, append = false) {
  loading.value = true
  searching.value = false
  try {
    const d = await api.fileList({
      driveId: driveId.value,
      path,
      orderBy: orderBy.value,
      order: order.value,
      marker: append ? nextMarker.value : undefined,
      limit: 200,
    })
    target.value = d.target
    cwd.value = d.target?.path || path
    files.value = append ? [...files.value, ...(d.files || [])] : d.files || []
    nextMarker.value = d.nextMarker || ''
    if (!append) clearSelection()
    notLogin.value = false
  } catch (e) {
    if (e.code === CODE_NOT_LOGIN) {
      notLogin.value = true
      files.value = []
    } else {
      toast.error(e.message)
    }
  } finally {
    loading.value = false
  }
}

async function doSearch() {
  const kw = keyword.value.trim()
  if (!kw) return load()
  loading.value = true
  try {
    const d = await api.fileSearch({ driveId: driveId.value, path: cwd.value, keyword: kw, limit: 300 })
    files.value = d.files || []
    nextMarker.value = ''
    searching.value = true
    clearSelection()
    if (d.truncated) toast.info('结果过多，已截断显示')
  } catch (e) {
    toast.error(e.message)
  } finally {
    loading.value = false
  }
}

function enter(f) {
  if (f.isFolder) {
    keyword.value = ''
    load(f.path)
  } else if (isPreviewable(f)) {
    preview.value = f
  } else {
    toast.info('该类型不支持预览，可使用「下载到服务器」或直接下载')
  }
}

function clearSelection() {
  selected.value = new Set()
  selTick.value++
}

function toggle(f) {
  const s = new Set(selected.value)
  if (s.has(f.path)) s.delete(f.path)
  else s.add(f.path)
  selected.value = s
  selTick.value++
}

function toggleAll() {
  selected.value = allChecked.value ? new Set() : new Set(files.value.map((f) => f.path))
  selTick.value++
}

function sortBy(col) {
  if (orderBy.value === col) order.value = order.value === 'asc' ? 'desc' : 'asc'
  else { orderBy.value = col; order.value = 'asc' }
  load()
}

async function switchDrive(id) {
  try {
    await account.switchDrive(id)
    driveId.value = id
    cwd.value = '/'
    keyword.value = ''
    load('/')
  } catch (e) {
    toast.error(e.message)
  }
}

// ---- 写操作 ----

async function doMkdir() {
  const name = prompt('新建文件夹名称')
  if (!name) return
  try {
    await api.mkdir(driveId.value, joinPath(cwd.value, name.trim()))
    toast.success('已创建')
    load()
  } catch (e) {
    toast.error(e.message)
  }
}

async function doRename(f) {
  const name = prompt('重命名为', f.fileName)
  if (!name || name === f.fileName) return
  try {
    await api.rename(driveId.value, f.path, name.trim())
    toast.success('已重命名')
    load()
  } catch (e) {
    toast.error(e.message)
  }
}

async function doDelete() {
  const list = selectedFiles.value
  if (!list.length) return
  if (!confirm(`确认删除选中的 ${list.length} 项？文件会进入回收站。`)) return
  try {
    const d = await api.remove(driveId.value, list.map((f) => f.path))
    const failed = (d.items || []).filter((i) => !i.success)
    if (failed.length) toast.error(`${failed.length} 项删除失败: ${failed[0].reason}`)
    else toast.success('已删除')
    load()
  } catch (e) {
    toast.error(e.message)
  }
}

function openPanPicker(mode) {
  if (!selectedFiles.value.length) return
  panPickerMode.value = mode
  showPanPicker.value = true
}

async function onPanDirPicked(dst) {
  const list = selectedFiles.value.map((f) => f.path)
  try {
    const fn = panPickerMode.value === 'copy' ? api.copy : api.move
    const d = await fn(driveId.value, list, dst)
    const failed = (d.items || []).filter((i) => !i.success)
    if (failed.length) toast.error(`${failed.length} 项失败: ${failed[0].reason}`)
    else toast.success(panPickerMode.value === 'copy' ? '已复制' : '已移动')
    load()
  } catch (e) {
    toast.error(e.message)
  }
}

// ---- 传输 ----

async function onDownloadTo(paths) {
  const list = selectedFiles.value.map((f) => f.path)
  if (!list.length) return
  try {
    await api.download({ driveId: driveId.value, panPaths: list, saveTo: paths[0] })
    toast.success('已加入下载队列')
    transfer.refresh().catch(() => {})
    router.push('/transfer')
  } catch (e) {
    toast.error(e.message)
  }
}

async function onUploadFrom(paths) {
  try {
    await api.upload({ driveId: driveId.value, panDir: cwd.value, localPaths: paths })
    toast.success('已加入上传队列')
    transfer.refresh().catch(() => {})
    router.push('/transfer')
  } catch (e) {
    toast.error(e.message)
  }
}

// 浏览器直传：先分片写到服务器暂存，再交给上传管线（秒传/断点都能用上）
async function onBrowserFiles(ev) {
  const list = [...(ev.target.files || [])]
  ev.target.value = ''
  for (const file of list) {
    await uploadOne(file)
  }
  if (list.length) {
    transfer.refresh().catch(() => {})
    router.push('/transfer')
  }
}

async function uploadOne(file) {
  uploading.value = { name: file.name, sent: 0, total: file.size }
  let uploadId = null
  try {
    const s = await api.uploadSessionCreate({
      driveId: driveId.value,
      panDir: cwd.value,
      fileName: file.name,
      size: file.size,
    })
    uploadId = s.uploadId
    const chunkSize = s.chunkSize || 8 * 1024 * 1024
    for (let offset = 0; offset < file.size; offset += chunkSize) {
      const blob = file.slice(offset, Math.min(offset + chunkSize, file.size))
      await api.uploadSessionChunk(uploadId, offset, blob)
      uploading.value = { name: file.name, sent: Math.min(offset + chunkSize, file.size), total: file.size }
    }
    await api.uploadSessionComplete(uploadId)
    toast.success(`${file.name} 已提交上传`)
  } catch (e) {
    toast.error(`${file.name} 上传失败: ${e.message}`)
    if (uploadId) await api.uploadSessionDelete(uploadId).catch(() => {})
  } finally {
    uploading.value = null
  }
}

function directDownload(f) {
  window.open(api.contentUrl({ driveId: driveId.value, fileId: f.fileId }), '_blank')
}
</script>

<template>
  <div>
    <h2 class="page-title">文件</h2>

    <div v-if="notLogin" class="card">
      <div class="card-body">
        <p>尚未登录阿里云盘账号。</p>
        <RouterLink class="btn primary" to="/account">前往账号页扫码登录</RouterLink>
      </div>
    </div>

    <template v-else>
      <div class="card">
        <div class="card-body">
          <div class="row" style="margin-bottom: 10px">
            <select
              :value="driveId"
              style="width: auto"
              @change="switchDrive($event.target.value)"
            >
              <option v-for="d in account.drives" :key="d.driveId" :value="d.driveId">
                {{ d.driveName }}
              </option>
            </select>

            <button class="btn sm" @click="load(parentPath(cwd))" :disabled="cwd === '/'">⬆ 上级</button>
            <button class="btn sm" @click="load()">↻ 刷新</button>
            <button class="btn sm" @click="doMkdir">＋ 新建文件夹</button>

            <div class="spacer"></div>

            <input
              v-model="keyword"
              type="text"
              placeholder="在当前目录下搜索…"
              style="width: 200px"
              @keyup.enter="doSearch"
            />
            <button class="btn sm" @click="doSearch">搜索</button>
          </div>

          <div class="crumbs">
            <template v-for="(c, i) in breadcrumbs(cwd)" :key="c.path">
              <span v-if="i" class="sep">/</span>
              <span v-if="i === breadcrumbs(cwd).length - 1" class="cur">{{ c.name }}</span>
              <button v-else @click="load(c.path)">{{ c.name }}</button>
            </template>
            <span v-if="searching" class="tag" style="margin-left: 8px">搜索结果</span>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-head">
          <span>{{ files.length }} 项</span>
          <template v-if="selectedFiles.length">
            <span class="muted small">已选 {{ selectedFiles.length }}</span>
            <button class="btn sm" @click="showLocalPicker = true">↓ 下载到服务器</button>
            <button class="btn sm" @click="openPanPicker('copy')">复制到</button>
            <button class="btn sm" @click="openPanPicker('move')">移动到</button>
            <button class="btn sm danger" @click="doDelete">删除</button>
          </template>
          <div class="spacer"></div>
          <button class="btn sm" @click="showUploadPicker = true">↑ 上传服务器文件</button>
          <label class="btn sm">
            ↑ 从浏览器上传
            <input type="file" multiple hidden @change="onBrowserFiles" />
          </label>
        </div>

        <div v-if="uploading" class="card-body" style="border-bottom: 1px solid var(--border)">
          <div class="small truncate">正在上传到服务器暂存: {{ uploading.name }}</div>
          <div class="progress" style="margin-top: 6px">
            <i :style="{ width: (uploading.sent / uploading.total) * 100 + '%' }" />
          </div>
        </div>

        <div v-if="loading && !files.length" class="empty">加载中…</div>
        <table v-else>
          <thead>
            <tr>
              <th style="width: 34px">
                <input type="checkbox" :checked="allChecked" @change="toggleAll" />
              </th>
              <th style="width: 30px"></th>
              <th style="cursor: pointer" @click="sortBy('name')">名称</th>
              <th style="width: 110px; cursor: pointer" @click="sortBy('size')">大小</th>
              <th style="width: 165px; cursor: pointer" class="hide-sm" @click="sortBy('updated_at')">
                修改时间
              </th>
              <th style="width: 96px"></th>
            </tr>
          </thead>
          <tbody :key="selTick">
            <tr
              v-for="f in files"
              :key="f.fileId || f.path"
              :class="{ selected: selected.has(f.path) }"
            >
              <td><input type="checkbox" :checked="selected.has(f.path)" @change="toggle(f)" /></td>
              <td>{{ fileIcon(f) }}</td>
              <td class="truncate" style="max-width: 1px">
                <a href="#" @click.prevent="enter(f)">{{ f.fileName }}</a>
                <div v-if="searching" class="small muted truncate">{{ f.path }}</div>
              </td>
              <td class="small muted">{{ f.isFolder ? '-' : formatSize(f.fileSize) }}</td>
              <td class="small muted hide-sm">{{ f.updatedAt }}</td>
              <td>
                <button class="btn sm" @click="doRename(f)">改名</button>
                <button v-if="!f.isFolder" class="btn sm" @click="directDownload(f)">⤓</button>
              </td>
            </tr>
            <tr v-if="!files.length && !loading">
              <td colspan="6" class="empty">这里什么都没有</td>
            </tr>
          </tbody>
        </table>

        <div v-if="nextMarker" class="card-body" style="text-align: center">
          <button class="btn" :disabled="loading" @click="load(cwd, true)">加载更多</button>
        </div>
      </div>
    </template>

    <LocalPathPicker
      v-model="showLocalPicker"
      title="选择服务器保存目录"
      :dirs-only="true"
      @confirm="onDownloadTo"
    />
    <LocalPathPicker
      v-model="showUploadPicker"
      title="选择要上传的服务器文件 / 目录"
      :dirs-only="false"
      @confirm="onUploadFrom"
    />
    <PanDirPicker
      v-model="showPanPicker"
      :drive-id="driveId"
      :title="panPickerMode === 'copy' ? '复制到…' : '移动到…'"
      @confirm="onPanDirPicked"
    />
    <FilePreview :file="preview" :drive-id="driveId" @close="preview = null" />
  </div>
</template>
