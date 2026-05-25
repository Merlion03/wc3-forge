<!-- @migration-task Error while migrating Svelte code: This migration would change the name of a slot (header-extras to header_extras) making the component unusable -->
<script lang="ts">
  // Generic collapsible section with a clickable header + chevron affordance.
  // Used in Explorer and Properties panels to let users hide sections they
  // aren't working with. State is owned by the parent (controlled component):
  // the parent maintains the open/closed map keyed by section id and toggles
  // it in the on:toggle handler.
  //
  // Layout model:
  //   <Accordion>
  //     <header>  ← always present, sticky-styled to match panel chrome
  //     <body>    ← present only when `open`; CSS transitions max-height
  //                  for a subtle 200ms ease open/close
  //
  // Slots:
  //   - "header-extras" — optional right-aligned content (e.g. a count badge)
  //   - default        — the body content
  //
  // The body uses a wrapper div with max-height: 0/9999px transition. We can't
  // animate to `auto`, so 9999px is the practical cap — fine for our panel
  // contents which rarely exceed a few hundred px.

  import { createEventDispatcher } from 'svelte'

  export let label: string = ''
  export let open: boolean = true
  // Optional id so the parent can key state to this section; just passed
  // through in the dispatched event so consumers can route a single handler
  // across all of their accordions.
  export let id: string = ''

  const dispatch = createEventDispatcher<{ toggle: { id: string; open: boolean } }>()

  function toggle() {
    open = !open
    dispatch('toggle', { id, open })
  }
  function onHeaderKey(e: KeyboardEvent) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      toggle()
    }
  }
</script>

<div class="accordion" class:open>
  <header class="acc-header"
          role="button"
          tabindex="0"
          aria-expanded={open}
          on:click={toggle}
          on:keydown={onHeaderKey}>
    <span class="chevron" aria-hidden="true">{open ? '▾' : '▸'}</span>
    <span class="label">{label}</span>
    <span class="extras"><slot name="header-extras" /></span>
  </header>
  {#if open}
    <div class="acc-body">
      <slot />
    </div>
  {/if}
</div>

<style>
  .accordion {
    display: flex; flex-direction: column;
    flex: 0 0 auto;
  }
  .acc-header {
    flex: 0 0 auto;
    display: flex; align-items: center; gap: 6px;
    padding: 6px 12px;
    background: #1c1c1f; border-bottom: 1px solid #27272a;
    color: #a1a1aa; font-size: 10px; font-weight: 600;
    text-transform: uppercase; letter-spacing: 0.06em;
    cursor: pointer;
    user-select: none;
    transition: background 120ms ease;
  }
  .acc-header:hover { background: #232328; color: #e4e4e7; }
  .acc-header:focus-visible {
    outline: 1px solid #2563eb; outline-offset: -1px;
  }
  .chevron {
    flex: 0 0 auto;
    width: 10px; color: #71717a; font-size: 10px;
    transition: transform 200ms ease;
  }
  .label { flex: 0 1 auto; min-width: 0; }
  .extras { flex: 1 1 auto; display: flex; justify-content: flex-end; gap: 6px; color: #71717a; font-weight: 400; text-transform: none; letter-spacing: 0; }
  .acc-body {
    flex: 0 0 auto;
    display: flex; flex-direction: column;
    animation: acc-open 200ms ease;
  }
  @keyframes acc-open {
    from { opacity: 0; transform: translateY(-2px); }
    to   { opacity: 1; transform: translateY(0); }
  }
</style>
