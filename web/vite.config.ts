import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:30000',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  test: {
    // Unit tests run in the plain node environment; only the store, api and
    // helper modules are covered, so no DOM shim is needed.
    environment: 'node',
    include: ['src/**/*.test.ts'],
    coverage: {
      provider: 'v8',
      include: ['src/api.ts', 'src/stores/**/*.ts', 'src/utils.ts'],
      // 测试本身不算进覆盖率，否则覆盖率会被断言代码稀释。
      exclude: ['src/**/*.test.ts'],
      reporter: ['text', 'json-summary', 'json'],
      reportsDirectory: 'coverage',
    },
  },
})
