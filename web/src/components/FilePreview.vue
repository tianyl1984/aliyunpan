<script setup>
import { computed, ref, watch } from 'vue'
import { api } from '../api'
import { fileKind } from '../utils'

const props = defineProps({
  file: { type: Object, default: null },
  driveId: { type: String, default: '' },
})
const emit = defineEmits(['close'])

const textContent = ref('')
const loadingText = ref(false)

const kind = computed(() => fileKind(props.file))
const url = computed(() =>
  props.file ? api.previewUrl({ driveId: props.driveId, fileId: props.file.fileId }) : '')
const downloadUrl = computed(() =>
  props.file ? api.contentUrl({ driveId: props.driveId, fileId: props.file.fileId }) : '')

watch(
  () => props.file,
  async (f) => {
    textContent.value = ''
    if (!f || fileKind(f) !== 'text') return
    // 文本预览限制在 512KB 内，避免把超大日志整个拉进浏览器
    if (f.fileSize > 512 * 1024) {
      textContent.value = '(文件过大，仅支持预览 512KB 以内的文本)'
      return
    }
    loadingText.value = true
    try {
      const resp = await fetch(url.value, { credentials: 'same-origin' })
      textContent.value = await resp.text()
    } catch (e) {
      textContent.value = `加载失败: ${e.message}`
    } finally {
      loadingText.value = false
    }
  },
  { immediate: true },
)
</script>

<template>
  <div v-if="file" class="mask" @click.self="emit('close')">
    <div class="modal wide">
      <div class="modal-head truncate">{{ file.fileName }}</div>
      <div class="modal-body" style="text-align: center">
        <img v-if="kind === 'image'" :src="url" style="max-width: 100%; max-height: 68vh" />
        <video v-else-if="kind === 'video'" :src="url" controls style="max-width: 100%; max-height: 68vh" />
        <audio v-else-if="kind === 'audio'" :src="url" controls style="width: 100%" />
        <iframe v-else-if="kind === 'pdf'" :src="url" style="width: 100%; height: 68vh; border: 0" />
        <pre v-else-if="kind === 'text'" class="console-out" style="text-align: left">{{
          loadingText ? '加载中…' : textContent
        }}</pre>
        <p v-else class="muted">该类型不支持在线预览，请下载后查看。</p>
      </div>
      <div class="modal-foot">
        <a class="btn" :href="downloadUrl" target="_blank" rel="noopener">下载到本机</a>
        <button class="btn primary" @click="emit('close')">关闭</button>
      </div>
    </div>
  </div>
</template>
