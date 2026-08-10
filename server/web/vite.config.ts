import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    proxy: {
      '/api': 'http://localhost:3000',
      '/ws': { target: 'ws://localhost:3000', ws: true },
    },
  },
  build: {
    // Route-split page chunks + already-dynamic heavy viewers (echarts, xlsx,
    // xterm, mammoth, pptx, hls/mpegts) keep most weight out of the entry. The
    // groups below pull the always-loaded framework + the markdown rendering
    // stack into long-lived, separately-cached vendor chunks so the first-paint
    // request stays small and repeat visits hit cache.
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (/[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/.test(id)) return 'react-vendor'
          if (id.includes('@tanstack')) return 'tanstack'
          if (id.includes('@radix-ui')) return 'radix'
          if (
            /[\\/](react-markdown|micromark|mdast|hast|unified|unist|remark|property-information|decode-named-character-reference|character-entities|trim-lines|space-separated-tokens|comma-separated-tokens|vfile|bail|trough|zwitch|html-void-elements)/.test(
              id,
            )
          ) {
            return 'markdown'
          }
        },
      },
    },
  },
})
