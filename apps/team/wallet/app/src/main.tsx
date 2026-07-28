import React from 'react'
import ReactDOM from 'react-dom/client'
import { GuiProvider, createGui } from '@hanzo/gui'
import { defaultConfig } from '@hanzogui/config/v5'
import { App } from './App'

const config = createGui(defaultConfig)

// Follow the OS scheme; the monochrome page tokens in index.html follow it too.
const light = window.matchMedia('(prefers-color-scheme: light)').matches

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <GuiProvider config={config} defaultTheme={light ? 'light' : 'dark'}>
      <App />
    </GuiProvider>
  </React.StrictMode>,
)
