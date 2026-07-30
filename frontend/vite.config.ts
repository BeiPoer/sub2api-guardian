import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// 构建产物直接写进后端的 embed 目录，`go build` 即可把面板打进单个二进制。
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  build: {
    outDir: '../backend/internal/web/dist',
    emptyOutDir: true
  },
  server: {
    host: '127.0.0.1',
    port: 5177,
    proxy: {
      '/api': {
        target: process.env.GUARDIAN_API_PROXY || 'http://127.0.0.1:8787',
        changeOrigin: true
      },
      '/healthz': {
        target: process.env.GUARDIAN_API_PROXY || 'http://127.0.0.1:8787',
        changeOrigin: true
      }
    }
  }
})
