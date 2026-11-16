import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// In development the API runs separately; set HLSMON_API to its address.
const api = process.env.HLSMON_API ?? 'http://127.0.0.1:4000'

export default defineConfig({
  // Relative asset links: the page may be published under a URL prefix,
  // which the server announces in a <base> element.
  base: './',
  plugins: [vue()],
  resolve: { alias: { '@': '/src' } },
  server: {
    proxy: { '/api': api, '/swagger': api, '/healthz': api },
  },
  build: {
    target: 'es2022',
    chunkSizeWarningLimit: 800,
  },
})
