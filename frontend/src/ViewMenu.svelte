<script lang="ts">
  // View menu — dropdown sibling to File menu in the header. Hosts visibility
  // toggles for doodad categories (and any future "show / hide" affordances).
  //
  // Currently shows:
  //   - Doodads → All        (tri-state checkbox; toggle every doodad instance)
  //   - Doodads → <Category> (checkbox per category present in the loaded map)
  //
  // Visibility state is OWNED by the parent (App.svelte). This component
  // calls `onToggle` with the affected category (or "*" for all) + desired
  // visibility. The parent flips its `doodadVisibility` Record and forwards
  // to scene.setDoodadCategoryVisible.
  //
  // The "All" checkbox is tri-state:
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

  interface Props {
    categories?: string[]
    visibility?: Record<string, boolean>
    onToggle?: (detail: { category: string; visible: boolean }) => void
  }

  let { categories = [], visibility = {}, onToggle }: Props = $props()

  let allVisibleCount = $derived(categories.filter(c => visibility[c] !== false).length)
  let allChecked = $derived(categories.length > 0 && allVisibleCount === categories.length)
  let allIndeterminate = $derived(allVisibleCount > 0 && allVisibleCount < categories.length)

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
  </DropdownMenu.Content>
</DropdownMenu.Root>
