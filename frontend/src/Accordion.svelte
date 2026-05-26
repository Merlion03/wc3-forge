<script lang="ts">
  // Generic collapsible section, now backed by shadcn-svelte's Accordion
  // primitives (bits-ui under the hood). Preserves the existing public API
  // as a drop-in replacement except that the hyphenated `header-extras` slot
  // is renamed to a `headerExtras` snippet — Svelte 5 snippets must be valid
  // JS identifiers.
  //
  // Behaviour parity vs. the original:
  //   - `open` is two-way bindable so the parent can drive it.
  //   - `onToggle({ id, open })` fires whenever the open state flips, matching
  //     the old `dispatch('toggle', ...)`.
  //   - Header label + optional right-aligned extras (badge / count).
  //   - Built-in chevron comes from the shadcn trigger (replaces our custom ▾/▸).
  //
  // Styling is entirely Tailwind + shadcn — the old scoped <style> block is gone.

  import type { Snippet } from 'svelte'
  import * as Accordion from '$lib/components/ui/accordion/index.js'

  let {
    id,
    label,
    open = $bindable(false),
    onToggle,
    headerExtras,
    children,
  }: {
    id: string
    label: string
    open?: boolean
    onToggle?: (detail: { id: string; open: boolean }) => void
    headerExtras?: Snippet
    children?: Snippet
  } = $props()

  // bits-ui's single-type Accordion.Root binds a string value: either the
  // open item's `value` (here, `id`) or "" when nothing is open. We mirror
  // that into the `open` boolean so the public API stays a clean bindable.
  let value = $state<string>(open ? id : '')

  // open → value
  $effect(() => {
    const next = open ? id : ''
    if (value !== next) value = next
  })

  // value → open + fire onToggle on a real change
  $effect(() => {
    const nextOpen = value === id
    if (nextOpen !== open) {
      open = nextOpen
      onToggle?.({ id, open: nextOpen })
    }
  })
</script>

<Accordion.Root type="single" bind:value class="cn-accordion-wrapper">
  <Accordion.Item value={id} class="border-b-0">
    <Accordion.Trigger
      class="flex w-full items-center gap-1.5 px-3 py-0.5 bg-muted border-b border-border text-[10px] font-semibold uppercase tracking-[0.06em] text-muted-foreground hover:bg-accent hover:text-accent-foreground hover:no-underline rounded-none"
    >
      <span class="min-w-0 flex-none">{label}</span>
      {#if headerExtras}
        <span class="ml-auto flex flex-1 justify-end gap-1.5 text-muted-foreground font-normal normal-case tracking-normal">
          {@render headerExtras()}
        </span>
      {/if}
    </Accordion.Trigger>
    <Accordion.Content class="p-0">
      {@render children?.()}
    </Accordion.Content>
  </Accordion.Item>
</Accordion.Root>
