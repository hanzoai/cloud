// Hanzogui v7 config for the Auto admin SPA. Same default config every
// Hanzo admin surface uses — no theme overrides. White-label colours
// land via /v1/auto/brand at runtime, not at build time.

import { defaultConfig } from '@hanzogui/config/v5'
import { createHanzogui } from 'hanzogui'

export const config = createHanzogui(defaultConfig)

export default config

export type Conf = typeof config

declare module 'hanzogui' {
  interface HanzoguiCustomConfig extends Conf {}
}
declare module '@hanzogui/web' {
  interface HanzoguiCustomConfig extends Conf {}
}
