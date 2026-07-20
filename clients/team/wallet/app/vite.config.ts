import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The team usage/wallet page. base matches the serve mount in clients/team
// (GET /v1/team/billing/ui/*) so every asset URL is absolute under the embed.
// @hanzo/gui runs RUNTIME-ONLY (no static extractor) — the same supported mode
// hanzoai/console uses via transpilePackages; the resolve/define blocks are the
// Vite equivalent (react-native → react-native-web, web conditions, GUI env).
export default defineConfig({
  plugins: [react()],
  base: '/v1/team/billing/ui/',
  define: {
    __DEV__: 'false',
    'process.env.NODE_ENV': JSON.stringify('production'),
    'process.env.GUI_TARGET': JSON.stringify('web'),
    'process.env.GUI_REACT_19': '"1"',
  },
  resolve: {
    alias: {
      'react-native': 'react-native-web',
      // Web-safe SVG shim — stock react-native-svg pulls fabric/codegen native
      // modules that do not exist on react-native-web.
      'react-native-svg': '@hanzogui/react-native-svg',
    },
    conditions: ['browser', 'module', 'import', 'default'],
    dedupe: ['react', 'react-dom', 'react-native-web', '@hanzo/gui'],
  },
  build: { outDir: '../dist', emptyOutDir: true, sourcemap: false, target: 'es2020' },
})
