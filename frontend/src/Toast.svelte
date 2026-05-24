<script lang="ts">
  import { toasts, dismissToast, type Toast } from './toast'

  // Severity → ARIA role. info is non-interruptive (status), warning/error
  // are assertive (alert) so screen readers announce them immediately.
  function ariaRole(severity: Toast['severity']): 'status' | 'alert' {
    return severity === 'info' ? 'status' : 'alert'
  }
</script>

<div class="toast-container" aria-live="polite">
  {#each $toasts as t (t.id)}
    <div class="toast {t.severity}" role={ariaRole(t.severity)}>
      <div class="toast-msg">{t.message}</div>
      <button class="toast-close"
              on:click={() => dismissToast(t.id)}
              title="Dismiss"
              aria-label="Dismiss notification">×</button>
    </div>
  {/each}
</div>

<style>
  /* Absolutely-positioned bottom-right stack. Above all panel content
     (z-index high enough to clear the explorer / properties asides). The
     container itself is pointer-events: none so it never blocks clicks on
     the viewport behind empty space — only the toast cards themselves
     receive pointer events. */
  .toast-container {
    position: fixed;
    bottom: 16px;
    right: 16px;
    display: flex;
    flex-direction: column-reverse;  /* newest at bottom of stack */
    gap: 8px;
    z-index: 1000;
    pointer-events: none;
    max-width: 420px;
  }

  .toast {
    pointer-events: auto;
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 8px 10px 8px 12px;
    background: #18181b;
    border: 1px solid #3f3f46;
    border-left-width: 3px;
    color: #e4e4e7;
    font-size: 12px;
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    box-shadow: 0 2px 8px rgba(0, 0, 0, 0.4);
    min-width: 240px;
    animation: toast-in 140ms ease-out;
  }

  /* Severity tones. Border-left accent + matching CSS variable on the message
     text keeps the visual language consistent with the existing Save-pill
     amber and the .error band's red. */
  .toast.info {
    border-left-color: #71717a;
  }
  .toast.warning {
    border-left-color: #b45309;  /* matches Save-pill dirty */
  }
  .toast.warning .toast-msg {
    color: #fde68a;
  }
  .toast.error {
    border-left-color: #7f1d1d;  /* matches .error band */
    background: #1f0f0f;
  }
  .toast.error .toast-msg {
    color: #fecaca;
  }

  .toast-msg {
    flex: 1 1 auto;
    white-space: pre-wrap;
    word-break: break-word;
    line-height: 1.4;
  }

  .toast-close {
    flex: 0 0 auto;
    background: transparent;
    border: 0;
    color: #71717a;
    cursor: pointer;
    font-size: 16px;
    line-height: 1;
    padding: 0 2px;
    margin-left: 4px;
  }
  .toast-close:hover { color: #e4e4e7; }

  @keyframes toast-in {
    from { opacity: 0; transform: translateY(4px); }
    to   { opacity: 1; transform: translateY(0); }
  }
</style>
