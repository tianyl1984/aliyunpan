<script setup>
// 服务器本地目录选择器。
// 只能浏览服务端 --local-root 白名单内的目录，越界会被服务端 403。
import { ref, watch } from 'vue'
import { api } from '../api'
import { useToastStore } from '../stores'
import { formatSize } from '../utils'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  title: { type: String, default: '选择服务器目录' },
  // dirsOnly: 只允许选目录（下载保存位置）；false 时也可以勾选文件（上传源）
  dirsOnly: { type: Boolean, default: true },
})
const emit = defineEmits(['update:modelValue', 'confirm'])

const toast = useToastStore()
const roots = ref([])
const entries = ref([])
const cwd = ref('')
const parent = ref('')
const loading = ref(false)
const picked = ref(new Set())
const tick = ref(0)

watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      picked.value = new Set()
      tick.value++
      loadRoots()
    }
  },
)

async function loadRoots() {
  loading.value = true
  try {
    const d = await api.localRoots()
    roots.value = d.roots || []
    if (roots.value.length) await open(roots.value[0].path)
  } catch (e) {
    toast.error(e.message)
  } finally {
    loading.value = false
  }
}

async function open(path) {
  loading.value = true
  try {
    const d = await api.localList({ path, dirsOnly: props.dirsOnly ? 1 : undefined })
    cwd.value = d.path
    parent.value = d.parent || ''
    entries.value = d.entries || []
  } catch (e) {
    toast.error(e.message)
  } finally {
    loading.value = false
  }
}

function toggle(entry) {
  if (props.dirsOnly) return
  const s = new Set(picked.value)
  if (s.has(entry.path)) s.delete(entry.path)
  else s.add(entry.path)
  picked.value = s
  tick.value++
}

function confirm() {
  if (props.dirsOnly) {
    emit('confirm', [cwd.value])
  } else {
    const list = [...picked.value]
    if (!list.length) {
      toast.error('请至少勾选一个文件或目录')
      return
    }
    emit('confirm', list)
  }
  emit('update:modelValue', false)
}
</script>

<template>
  <div v-if="modelValue" class="mask" @click.self="emit('update:modelValue', false)">
    <div class="modal wide">
      <div class="modal-head">{{ title }}</div>
      <div class="modal-body">
        <div class="row" style="margin-bottom: 10px">
          <select
            :value="roots.find((r) => cwd.startsWith(r.path))?.path || ''"
            style="width: auto"
            @change="open($event.target.value)"
          >
            <option v-for="r in roots" :key="r.path" :value="r.path">{{ r.path }}</option>
          </select>
          <button class="btn sm" :disabled="!parent" @click="open(parent)">⬆ 上级</button>
          <span class="mono small truncate" style="flex: 1">{{ cwd }}</span>
        </div>

        <div v-if="loading" class="empty">加载中…</div>
        <table v-else>
          <thead>
            <tr>
              <th v-if="!dirsOnly" style="width: 34px"></th>
              <th>名称</th>
              <th style="width: 110px">大小</th>
              <th style="width: 165px" class="hide-sm">修改时间</th>
            </tr>
          </thead>
          <tbody :key="tick">
            <tr v-for="e in entries" :key="e.path">
              <td v-if="!dirsOnly">
                <input type="checkbox" :checked="picked.has(e.path)" @change="toggle(e)" />
              </td>
              <td>
                <a v-if="e.isDir" href="#" @click.prevent="open(e.path)">📁 {{ e.name }}</a>
                <span v-else>📄 {{ e.name }}</span>
              </td>
              <td class="small muted">{{ e.isDir ? '-' : formatSize(e.size) }}</td>
              <td class="small muted hide-sm">{{ e.modTime }}</td>
            </tr>
            <tr v-if="!entries.length">
              <td :colspan="dirsOnly ? 3 : 4" class="empty">目录为空</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="modal-foot">
        <span class="small muted" style="margin-right: auto">
          {{ dirsOnly ? '将使用当前目录作为保存位置' : `已选 ${picked.size} 项` }}
        </span>
        <button class="btn" @click="emit('update:modelValue', false)">取消</button>
        <button class="btn primary" @click="confirm">确定</button>
      </div>
    </div>
  </div>
</template>
