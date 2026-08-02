export function formatSize(bytes) {
  if (bytes === null || bytes === undefined) return '-'
  const n = Number(bytes)
  if (!Number.isFinite(n) || n < 0) return '-'
  if (n === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1)
  const v = n / Math.pow(1024, i)
  return `${v.toFixed(i === 0 ? 0 : v >= 100 ? 0 : v >= 10 ? 1 : 2)} ${units[i]}`
}

export function formatSpeed(bps) {
  if (!bps) return '-'
  return `${formatSize(bps)}/s`
}

export function formatDuration(ms) {
  if (!ms || ms < 0) return '-'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${s % 60}s`
  const h = Math.floor(m / 60)
  return `${h}h${m % 60}m`
}

export function formatTime(ms) {
  if (!ms) return '-'
  const d = new Date(ms)
  const p = (v) => String(v).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function parentPath(p) {
  if (!p || p === '/') return '/'
  const cleaned = p.replace(/\/+$/, '')
  const i = cleaned.lastIndexOf('/')
  return i <= 0 ? '/' : cleaned.slice(0, i)
}

export function joinPath(dir, name) {
  if (!dir || dir === '/') return `/${name}`
  return `${dir.replace(/\/+$/, '')}/${name}`
}

export function baseName(p) {
  if (!p || p === '/') return '/'
  const cleaned = p.replace(/\/+$/, '')
  const i = cleaned.lastIndexOf('/')
  return i < 0 ? cleaned : cleaned.slice(i + 1)
}

export function breadcrumbs(p) {
  const out = [{ name: '根目录', path: '/' }]
  if (!p || p === '/') return out
  let cur = ''
  p.split('/').filter(Boolean).forEach((seg) => {
    cur += `/${seg}`
    out.push({ name: seg, path: cur })
  })
  return out
}

const IMAGE_EXT = ['jpg', 'jpeg', 'png', 'gif', 'webp', 'bmp', 'svg', 'avif']
const VIDEO_EXT = ['mp4', 'mkv', 'mov', 'webm', 'avi', 'flv', 'm4v']
const AUDIO_EXT = ['mp3', 'flac', 'wav', 'aac', 'm4a', 'ogg']
const TEXT_EXT = ['txt', 'md', 'log', 'json', 'xml', 'yaml', 'yml', 'ini', 'conf', 'csv']

export function fileKind(file) {
  if (!file) return 'other'
  if (file.isFolder) return 'folder'
  const ext = (file.fileExtension || '').toLowerCase()
  if (IMAGE_EXT.includes(ext)) return 'image'
  if (VIDEO_EXT.includes(ext)) return 'video'
  if (AUDIO_EXT.includes(ext)) return 'audio'
  if (TEXT_EXT.includes(ext)) return 'text'
  if (ext === 'pdf') return 'pdf'
  return 'other'
}

export function isPreviewable(file) {
  return ['image', 'video', 'audio', 'text', 'pdf'].includes(fileKind(file))
}

const KIND_ICON = {
  folder: '📁',
  image: '🖼️',
  video: '🎬',
  audio: '🎵',
  text: '📄',
  pdf: '📕',
  other: '📦',
}

export function fileIcon(file) {
  return KIND_ICON[fileKind(file)] || KIND_ICON.other
}

export function percent(done, total) {
  if (!total || total <= 0) return 0
  return Math.min(100, Math.round((done / total) * 1000) / 10)
}
