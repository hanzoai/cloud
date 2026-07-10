// Hanzo Auto admin SPA. Vite + @hanzogui/admin shell.
//
// Build: emits into ../internal/ui/dist so the Go binary's //go:embed
// picks it up at the compiler stage without a copy step.
//
// Dev:   `pnpm dev` boots Vite at :5173 proxying /v1/* + /healthz to
//        the local Go server (default 127.0.0.1:8090). Set
//        VITE_AUTO_TARGET to point at auto.hanzo.ai etc.
//
// Hanzogui static extractor must stay enabled — without resolved
// theme CSS, runtime getThemeProxied() throws and the page is blank.

import { defineConfig } from 'vite'
import path from 'node:path'
import react from '@vitejs/plugin-react'
import { hanzoguiPlugin } from '@hanzogui/vite-plugin'

const TARGET = process.env.VITE_AUTO_TARGET ?? 'http://127.0.0.1:8090'

// `defineConfig` is typed as `any` here because @hanzogui/vite-plugin
// and Vite 8 each export a slightly different `Plugin` generic shape
// that TS can't compare without exploding (TS2321 excessive stack
// depth). The runtime contract is identical; the cast keeps tsc quiet
// without losing the editor IntelliSense the helper provides at the
// call site.
const plugins: unknown[] = []
plugins.push(
  hanzoguiPlugin({
    components: ['hanzogui'],
    config: path.resolve(__dirname, 'gui.config.ts'),
  }),
)
plugins.push(react())

const userConfig: any = {
  plugins,
  base: '/',
  define: {
    __DEV__: process.env.NODE_ENV !== 'production' ? 'true' : 'false',
    'process.env.HANZOGUI_TARGET': JSON.stringify('web'),
    'process.env.HANZOGUI_REACT_19': '"1"',
  },
  resolve: {
    alias: {
      'react-native': 'react-native-web',
      'react-native-svg': '@hanzogui/react-native-svg',
    },
    // `source` first so unbuilt workspace packages (`@hanzogui/admin`
    // ships raw TypeScript) resolve via `./src/index.ts` rather than a
    // missing `./dist`.
    conditions: ['source', 'browser', 'module', 'import', 'default'],
    dedupe: [
      'react',
      'react-dom',
      'react-native-web',
      'hanzogui',
      '@hanzogui/core',
      '@hanzogui/web',
      '@hanzogui/themes',
      '@hanzogui/use-element-layout',
    ],
  },
  optimizeDeps: {
    include: [
      'react',
      'react-dom',
      'react-dom/client',
      'react/jsx-runtime',
      'react-native-web',
      'hanzogui',
      '@hanzogui/core',
      '@hanzogui/web',
      '@hanzogui/themes',
      '@hanzogui/lucide-icons-2',
    ],
    esbuildOptions: {
      resolveExtensions: ['.web.tsx', '.web.ts', '.web.jsx', '.web.js', '.tsx', '.ts', '.jsx', '.js'],
      loader: { '.js': 'jsx' },
    },
  },
  build: {
    // Emit into internal/ui/dist so Go's //go:embed picks it up at the
    // compiler stage without a copy step.
    outDir: '../internal/ui/dist',
    emptyOutDir: true,
    sourcemap: false,
    target: 'es2020',
  },
  server: {
    port: 5173,
    proxy: {
      '/v1': { target: TARGET, changeOrigin: true, ws: true },
      '/healthz': TARGET,
      '/health': TARGET,
    },
  },
}

export default defineConfig(userConfig)
