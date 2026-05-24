// Transient operational error / info / warning notifications.
//
// Persistent state errors (e.g. "scene init failed") still live in the top
// error band inside App.svelte. Anything that fires from a user-triggered
// action (Save, Open, MoveUnit, toggleReforged…) is transient — it belongs
// in a toast that auto-dismisses, NOT a band that sticks until dismissed.
//
// API: showToast(message, severity?, durationMs?)
//   - severity defaults to 'info' (neutral gray)
//   - durationMs defaults to TOAST_DEFAULT_MS (5s); pass 0 to disable auto-dismiss
//
// Implementation notes:
//   - Single writable store, the <Toast /> renderer subscribes to it.
//   - Max TOAST_MAX visible at once; older toasts drop off the bottom of the
//     stack when a new one arrives over the cap.
//   - Timers are keyed by toast id so dismissToast(id) cancels the auto-timer.

import { writable } from 'svelte/store'

export type ToastSeverity = 'info' | 'warning' | 'error'

export interface Toast {
  id: number
  message: string
  severity: ToastSeverity
  durationMs: number  // 0 → no auto-dismiss
}

export const TOAST_DEFAULT_MS = 5000
export const TOAST_MAX = 3

let nextId = 1
const timers = new Map<number, ReturnType<typeof setTimeout>>()

export const toasts = writable<Toast[]>([])

export function showToast(
  message: string,
  severity: ToastSeverity = 'info',
  durationMs: number = TOAST_DEFAULT_MS,
): number {
  const id = nextId++
  const toast: Toast = { id, message, severity, durationMs }
  toasts.update(list => {
    // Stack newest at the top of the array; the renderer flexes column-reverse
    // so visually newest is at the bottom (closer to where the user's eye is
    // for the most-recent message). When over cap, drop the OLDEST.
    const next = [toast, ...list]
    if (next.length > TOAST_MAX) {
      // Cancel timers for any toasts we're dropping.
      for (const dropped of next.slice(TOAST_MAX)) {
        const t = timers.get(dropped.id)
        if (t) { clearTimeout(t); timers.delete(dropped.id) }
      }
      return next.slice(0, TOAST_MAX)
    }
    return next
  })
  if (durationMs > 0) {
    timers.set(id, setTimeout(() => dismissToast(id), durationMs))
  }
  return id
}

export function dismissToast(id: number): void {
  const t = timers.get(id)
  if (t) { clearTimeout(t); timers.delete(id) }
  toasts.update(list => list.filter(x => x.id !== id))
}
