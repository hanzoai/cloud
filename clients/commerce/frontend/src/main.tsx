// Entry point. HanzoguiProvider wraps the entire app so theme tokens
// resolve before any shell component renders.

import React from 'react'
import ReactDOM from 'react-dom/client'
import { HanzoguiProvider } from 'hanzogui'
import config from '../gui.config'
import App from './App'

const root = document.getElementById('root')
if (!root) throw new Error('root element missing')

ReactDOM.createRoot(root).render(
  <React.StrictMode>
    <HanzoguiProvider config={config} defaultTheme="dark">
      <App />
    </HanzoguiProvider>
  </React.StrictMode>,
)
