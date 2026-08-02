import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 产物必须落在 internal/webui/assets/dist：
// go:embed 只能嵌入所在包目录及其子目录。
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../internal/webui/assets/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 900,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: false,
      },
    },
  },
})
