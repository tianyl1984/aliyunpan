<script setup>
import { ref, onMounted } from 'vue'
import { api } from '../api'
import { useToastStore } from '../stores'
import { formatSize } from '../utils'

const toast = useToastStore()
const form = ref(null)
const limits = ref({})
const sys = ref(null)
const saving = ref(false)

onMounted(load)

async function load() {
  try {
    const d = await api.configGet()
    form.value = { ...d.config }
    limits.value = d.limits || {}
    sys.value = await api.systemInfo()
  } catch (e) {
    toast.error(e.message)
  }
}

async function save() {
  saving.value = true
  try {
    const d = await api.configUpdate({
      cacheSize: Number(form.value.cacheSize) || 0,
      maxDownloadParallel: Number(form.value.maxDownloadParallel) || 0,
      maxUploadParallel: Number(form.value.maxUploadParallel) || 0,
      maxDownloadRate: Number(form.value.maxDownloadRate) || 0,
      maxUploadRate: Number(form.value.maxUploadRate) || 0,
      saveDir: form.value.saveDir || '',
      proxy: form.value.proxy || '',
      localAddrs: form.value.localAddrs || '',
      preferIPType: form.value.preferIPType || '',
      videoFileExtensions: form.value.videoFileExtensions || '',
      fileRecordConfig: form.value.fileRecordConfig || '2',
      deviceName: form.value.deviceName || '',
    })
    form.value = { ...d.config }
    toast.success('已保存')
  } catch (e) {
    toast.error(e.message)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div>
    <h2 class="page-title">设置</h2>

    <div v-if="form" class="card">
      <div class="card-head">
        传输配置
        <div class="spacer"></div>
        <button class="btn primary sm" :disabled="saving" @click="save">
          {{ saving ? '保存中…' : '保存' }}
        </button>
      </div>
      <div class="card-body">
        <table>
          <tbody>
            <tr>
              <td style="width: 210px">下载缓存（字节）</td>
              <td><input v-model="form.cacheSize" type="number" min="0" /></td>
              <td class="small muted hide-sm">0 表示使用默认值 64KB</td>
            </tr>
            <tr>
              <td>最大下载并发</td>
              <td><input v-model="form.maxDownloadParallel" type="number" min="0"
                :max="limits.maxFileDownloadParallelNum" /></td>
              <td class="small muted hide-sm">
                0 ~ {{ limits.maxFileDownloadParallelNum }}，超过会被阿里风控
              </td>
            </tr>
            <tr>
              <td>最大上传并发</td>
              <td><input v-model="form.maxUploadParallel" type="number" min="0"
                :max="limits.maxFileUploadParallelNum" /></td>
              <td class="small muted hide-sm">0 ~ {{ limits.maxFileUploadParallelNum }}</td>
            </tr>
            <tr>
              <td>下载限速（B/s）</td>
              <td><input v-model="form.maxDownloadRate" type="number" min="0" /></td>
              <td class="small muted hide-sm">
                0 不限制{{ form.maxDownloadRate > 0 ? `，当前 ${formatSize(form.maxDownloadRate)}/s` : '' }}
              </td>
            </tr>
            <tr>
              <td>上传限速（B/s）</td>
              <td><input v-model="form.maxUploadRate" type="number" min="0" /></td>
              <td class="small muted hide-sm">
                0 不限制{{ form.maxUploadRate > 0 ? `，当前 ${formatSize(form.maxUploadRate)}/s` : '' }}
              </td>
            </tr>
            <tr>
              <td>默认下载目录</td>
              <td><input v-model="form.saveDir" type="text" /></td>
              <td class="small muted hide-sm">留空使用系统默认</td>
            </tr>
            <tr>
              <td>代理</td>
              <td><input v-model="form.proxy" type="text" placeholder="http://127.0.0.1:8888" /></td>
              <td class="small muted hide-sm">支持 http / socks5</td>
            </tr>
            <tr>
              <td>本地网卡地址</td>
              <td><input v-model="form.localAddrs" type="text" /></td>
              <td class="small muted hide-sm">多个地址用逗号分隔</td>
            </tr>
            <tr>
              <td>IP 优先类型</td>
              <td>
                <select v-model="form.preferIPType">
                  <option value="">自动</option>
                  <option value="ipv4">IPv4</option>
                  <option value="ipv6">IPv6</option>
                </select>
              </td>
              <td class="small muted hide-sm">修改后需重启生效</td>
            </tr>
            <tr>
              <td>文件传输记录</td>
              <td>
                <select v-model="form.fileRecordConfig">
                  <option value="1">开启</option>
                  <option value="2">禁用</option>
                </select>
              </td>
              <td class="small muted hide-sm">开启后结果会写入 CSV</td>
            </tr>
            <tr>
              <td>客户端名称</td>
              <td><input v-model="form.deviceName" type="text" /></td>
              <td class="small muted hide-sm">修改后需重启生效</td>
            </tr>
            <tr>
              <td>视频扩展名</td>
              <td><input v-model="form.videoFileExtensions" type="text" /></td>
              <td class="small muted hide-sm">逗号分隔</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-if="sys" class="card">
      <div class="card-head">运行信息</div>
      <table>
        <tbody>
          <tr><td style="width: 210px">版本</td><td class="mono small">{{ sys.version }}</td></tr>
          <tr><td>平台</td><td class="mono small">{{ sys.os }}/{{ sys.arch }}</td></tr>
          <tr><td>监听地址</td><td class="mono small">{{ sys.listenAddr }}</td></tr>
          <tr><td>配置目录</td><td class="mono small">{{ sys.configDir }}</td></tr>
          <tr><td>日志目录</td><td class="mono small">{{ sys.logDir }}</td></tr>
          <tr><td>默认下载目录</td><td class="mono small">{{ sys.defaultSaveDir }}</td></tr>
          <tr>
            <td>可浏览的本地目录</td>
            <td class="mono small">{{ (sys.localRoots || []).join('\n') }}</td>
          </tr>
          <tr>
            <td>控制台执行系统命令</td>
            <td class="small">{{ sys.allowShell ? '已允许 (--allow-shell)' : '已禁用' }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
