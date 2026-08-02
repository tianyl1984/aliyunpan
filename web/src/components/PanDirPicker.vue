<script setup>
// 网盘目录选择器，用于复制/移动的目标目录与上传的目标目录
import { ref, watch } from 'vue'
import { api } from '../api'
import { useToastStore } from '../stores'
import { breadcrumbs } from '../utils'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  driveId: { type: String, default: '' },
  title: { type: String, default: '选择网盘目录' },
})
const emit = defineEmits(['update:modelValue', 'confirm'])

const toast = useToastStore()
const cwd = ref('/')
const dirs = ref([])
const loading = ref(false)

watch(
  () => props.modelValue,
  (open) => {
    if (open) load('/')
  },
)

async function load(path) {
  loading.value = true
  try {
    const d = await api.fileList({ driveId: props.driveId, path, limit: 1000 })
    cwd.value = d.target?.path || path
    dirs.value = (d.files || []).filter((f) => f.isFolder)
  } catch (e) {
    toast.error(e.message)
  } finally {
    loading.value = false
  }
}

function confirm() {
  emit('confirm', cwd.value)
  emit('update:modelValue', false)
}
</script>

<template>
  <div v-if="modelValue" class="mask" @click.self="emit('update:modelValue', false)">
    <div class="modal">
      <div class="modal-head">{{ title }}</div>
      <div class="modal-body">
        <div class="crumbs" style="margin-bottom: 10px">
          <template v-for="(c, i) in breadcrumbs(cwd)" :key="c.path">
            <span v-if="i" class="sep">/</span>
            <span v-if="i === breadcrumbs(cwd).length - 1" class="cur">{{ c.name }}</span>
            <button v-else @click="load(c.path)">{{ c.name }}</button>
          </template>
        </div>
        <div v-if="loading" class="empty">加载中…</div>
        <table v-else>
          <tbody>
            <tr v-for="d in dirs" :key="d.fileId">
              <td><a href="#" @click.prevent="load(d.path)">📁 {{ d.fileName }}</a></td>
            </tr>
            <tr v-if="!dirs.length"><td class="empty">没有子目录</td></tr>
          </tbody>
        </table>
      </div>
      <div class="modal-foot">
        <span class="small muted mono truncate" style="margin-right: auto">{{ cwd }}</span>
        <button class="btn" @click="emit('update:modelValue', false)">取消</button>
        <button class="btn primary" @click="confirm">选择当前目录</button>
      </div>
    </div>
  </div>
</template>
