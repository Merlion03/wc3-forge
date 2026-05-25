<script lang="ts">
  // View menu — dropdown sibling to File menu in the header. Hosts visibility
  // toggles and editor-wide view options.
  //
  // Layout (top → bottom):
  //   - Extras            (flat top-level checkboxes — e.g. Reforged Graphics,
  //                       Agent Console)
  //   - Doodads sub-menu  (tri-state "All" + one checkbox per category present)
  //   - Overlays sub-menu (curated OVERLAYS list — Pathing today, more later)
  //   - Theme sub-menu    (Light / Dark / System radio group)
  //
  // Visibility / theme state is OWNED by the parent (App.svelte). This
  // component fires the matching callback so the parent can flip its store and
  // forward to the scene / dom.
  //
  // The Doodads "All" checkbox is tri-state:
  //   - checked         all categories visible
  //   - unchecked       all categories hidden
  //   - indeterminate   mixed
  //
  // Sub-categories follow the order defined in App.svelte (DOODAD_CAT_ORDER)
  // — passed in via `categories` so we don't duplicate the curation here.

  import * as DropdownMenu from '$lib/components/ui/dropdown-menu'
  import { Button } from '$lib/components/ui/button'
  import { Checkbox } from '$lib/components/ui/checkbox'
  import ChevronDownIcon from '@lucide/svelte/icons/chevron-down'
  import SunIcon from '@lucide/svelte/icons/sun'
  import MoonIcon from '@lucide/svelte/icons/moon'
  import MonitorIcon from '@lucide/svelte/icons/monitor'

  type Theme = 'light' | 'dark' | 'system'

  interface Extra {
    id: string
    label: string
    checked: boolean
    title?: string
    disabled?: boolean
  }

  interface Props {
    categories?: string[]
    visibility?: Record<string, boolean>
    overlays?: Record<string, boolean>
    extras?: Extra[]
    theme?: Theme
    onToggle?: (detail: { category: string; visible: boolean }) => void
    onOverlayToggle?: (detail: { overlay: string; visible: boolean }) => void
    onExtraToggle?: (detail: { id: string; checked: boolean }) => void
    onThemeChange?: (theme: Theme) => void
  }

  let {
    categories = [],
    visibility = {},
    overlays = {},
    extras = [],
    theme = 'system',
    onToggle,
    onOverlayToggle,
    onExtraToggle,
    onThemeChange,
  }: Props = $props()

  // Curated list of overlays surfaced in the menu. id is the wire key; label
  // is the human-readable name. Extend as new overlays are added.
  const OVERLAYS: { id: string; label: string; title: string }[] = [
    { id: 'pathing', label: 'Pathing', title: 'Toggle the static pathing-map overlay (red=unwalkable, blue=unflyable, yellow=unbuildable).' },
  ]

  let allVisibleCount = $derived(categories.filter(c => visibility[c] !== false).length)
  let allChecked = $derived(categories.length > 0 && allVisibleCount === categories.length)
  let allIndeterminate = $derived(allVisibleCount > 0 && allVisibleCount < categories.length)
  let overlaysOnCount = $derived(OVERLAYS.filter(o => overlays[o.id] === true).length)

  function setAll(visible: boolean) {
    onToggle?.({ category: '*', visible })
  }
  function setCat(category: string, visible: boolean) {
    onToggle?.({ category, visible })
  }
  function onAllClick() {
    // Clicked while fully-checked OR indeterminate → hide everything.
    // Clicked while fully-unchecked → show everything.
    setAll(!(allChecked || allIndeterminate))
  }
  function onThemeValueChange(v: string) {
    if (v === 'light' || v === 'dark' || v === 'system') {
      onThemeChange?.(v)
    }
  }
</script>

<DropdownMenu.Root>
  <DropdownMenu.Trigger>
    {#snippet child({ props })}
      <Button
        {...props}
        variant="ghost"
        size="sm"
        title="View menu"
      >
        View
        <ChevronDownIcon class="text-muted-foreground" />
      </Button>
    {/snippet}
  </DropdownMenu.Trigger>
  <DropdownMenu.Content class="min-w-[220px]" align="start">
    {#if extras.length > 0}
      {#each extras as e (e.id)}
        <DropdownMenu.CheckboxItem
          checked={e.checked}
          onCheckedChange={(v) => onExtraToggle?.({ id: e.id, checked: v })}
          closeOnSelect={false}
          disabled={e.disabled === true}
          title={e.title}
        >
          {e.label}
        </DropdownMenu.CheckboxItem>
      {/each}
      <DropdownMenu.Separator />
    {/if}

    <DropdownMenu.Sub>
      <DropdownMenu.SubTrigger>
        <span class="flex-1">Doodads</span>
        <span class="text-muted-foreground ml-2 font-mono text-xs">
          {allVisibleCount}/{categories.length}
        </span>
      </DropdownMenu.SubTrigger>
      <DropdownMenu.SubContent class="max-h-[320px] min-w-[200px] overflow-y-auto">
        {#if categories.length === 0}
          <div class="text-muted-foreground px-2 py-1.5 text-xs italic">
            No doodads in map
          </div>
        {:else}
          <DropdownMenu.Item
            closeOnSelect={false}
            onSelect={onAllClick}
            class="gap-2"
          >
            <Checkbox
              checked={allChecked}
              indeterminate={allIndeterminate}
              tabindex={-1}
              aria-label="Toggle all doodad categories"
            />
            <span class="flex-1">All</span>
          </DropdownMenu.Item>
          <DropdownMenu.Separator />
          {#each categories as cat (cat)}
            <DropdownMenu.CheckboxItem
              checked={visibility[cat] !== false}
              onCheckedChange={(v) => setCat(cat, v)}
              closeOnSelect={false}
            >
              {cat}
            </DropdownMenu.CheckboxItem>
          {/each}
        {/if}
      </DropdownMenu.SubContent>
    </DropdownMenu.Sub>

    <DropdownMenu.Sub>
      <DropdownMenu.SubTrigger>
        <span class="flex-1">Overlays</span>
        <span class="text-muted-foreground ml-2 font-mono text-xs">
          {overlaysOnCount}/{OVERLAYS.length}
        </span>
      </DropdownMenu.SubTrigger>
      <DropdownMenu.SubContent class="min-w-[200px]">
        {#each OVERLAYS as o (o.id)}
          <DropdownMenu.CheckboxItem
            checked={overlays[o.id] === true}
            onCheckedChange={(v) => onOverlayToggle?.({ overlay: o.id, visible: v })}
            closeOnSelect={false}
            title={o.title}
          >
            {o.label}
          </DropdownMenu.CheckboxItem>
        {/each}
      </DropdownMenu.SubContent>
    </DropdownMenu.Sub>

    <DropdownMenu.Separator />

    <DropdownMenu.Sub>
      <DropdownMenu.SubTrigger>
        <span class="flex-1">Theme</span>
      </DropdownMenu.SubTrigger>
      <DropdownMenu.SubContent class="min-w-[160px]">
        <DropdownMenu.RadioGroup value={theme} onValueChange={onThemeValueChange}>
          <DropdownMenu.RadioItem value="light">
            <SunIcon />
            <span>Light</span>
          </DropdownMenu.RadioItem>
          <DropdownMenu.RadioItem value="dark">
            <MoonIcon />
            <span>Dark</span>
          </DropdownMenu.RadioItem>
          <DropdownMenu.RadioItem value="system">
            <MonitorIcon />
            <span>System</span>
          </DropdownMenu.RadioItem>
        </DropdownMenu.RadioGroup>
      </DropdownMenu.SubContent>
    </DropdownMenu.Sub>
  </DropdownMenu.Content>
</DropdownMenu.Root>
