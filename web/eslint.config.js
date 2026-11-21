// ESLint 配置：Vue 3 + TypeScript 前端。
//
// 目标（与 Clawbench 主干一致）：让 CI 能拦住真正的错误——未使用的变量、
// 模板里写错的名字、Vue 响应式对象被当成布尔值使用、残留的 console 调试。
// 不追求做格式化工具，排版交给编辑器。
import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import vuePlugin from 'eslint-plugin-vue'
import vueParser from 'vue-eslint-parser'
import globals from 'globals'

export default tseslint.config(
  {
    ignores: ['dist/**', 'node_modules/**', 'coverage/**', '*.config.ts', '*.config.js'],
  },

  js.configs.recommended,
  ...tseslint.configs.recommended,

  {
    languageOptions: {
      globals: {
        ...globals.browser,
      },
    },
  },

  {
    files: ['**/*.vue'],
    plugins: {
      vue: vuePlugin,
    },
    languageOptions: {
      parser: vueParser,
      parserOptions: {
        parser: tseslint.parser,
        ecmaVersion: 'latest',
        sourceType: 'module',
      },
    },
    processor: vuePlugin.processors['.vue'],
    rules: {
      'vue/multi-word-component-names': 'off',
      // `isWideScreen || ...` 这种写法：ref 对象永远为真，脚本里又不会自动
      // 解包，会把“按屏幕宽度判断”悄悄变成恒真。vue-tsc 也查不出这类错误。
      'vue/no-ref-as-operand': 'error',
      // typescript-eslint 默认信任 tsc 去查未定义变量，但 .vue 里的模板/脚本
      // 并不总会经过严格类型检查，所以这里显式打开。
      'no-undef': 'error',
      // 在 <template> 里调用函数时，一些参数故意不参与渲染（例如把事件对象
      // 直接忽略），不需要为此给每个参数加 _ 前缀。
      'no-unused-vars': 'off',
    },
  },

  {
    files: ['**/*.ts'],
    languageOptions: {
      parser: tseslint.parser,
      parserOptions: {
        ecmaVersion: 'latest',
        sourceType: 'module',
      },
    },
  },

  {
    rules: {
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_' }],
      'no-console': ['error', { allow: ['warn', 'error'] }],
      'no-empty': ['error', { allowEmptyCatch: true }],
    },
  },
)
