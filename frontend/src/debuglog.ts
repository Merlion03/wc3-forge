// Frontend → Go log bridge. Wails GUI apps have no devtools console in
// production builds, so we route diagnostics through a bound Go method
// that writes to a file next to the executable (wc3-forge.log).

import { LogJS } from '../wailsjs/go/main/App.js'

function fmt(args: unknown[]): string {
  return args
    .map(a => {
      if (a instanceof Error) return `${a.name}: ${a.message}\n${a.stack || ''}`
      if (typeof a === 'object') {
        try { return JSON.stringify(a) } catch { return String(a) }
      }
      return String(a)
    })
    .join(' ')
}

export function flog(...args: unknown[]) {
  try { LogJS(fmt(args)) } catch { /* ignore */ }
  // also surface to dev-mode console if available
  try { console.log(...args) } catch { /* ignore */ }
}

// Catch otherwise-silent module-load / runtime errors.
if (typeof window !== 'undefined') {
  window.addEventListener('error', e => {
    flog('window.error:', e.message, 'at', e.filename + ':' + e.lineno + ':' + e.colno, e.error)
  })
  window.addEventListener('unhandledrejection', e => {
    flog('unhandledrejection:', e.reason)
  })
}
