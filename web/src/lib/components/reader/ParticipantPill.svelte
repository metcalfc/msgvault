<script lang="ts">
  import { Menu, MenuContent, MenuItem, MenuTrigger, copyToClipboard } from '@kenn-io/kit-ui';

  import type { APIClient } from '../../api/client';
  import { parseAddress } from '../../reader/address';
  import { resolveParticipantID } from '../../reader/resolve-participant';

  interface Props {
    /** "Name <address>" or a bare address or phone number. */
    value: string;
    client?: APIClient;
    /** Opens the person's relationship view. */
    onOpenPerson?: (participantID: number) => void;
    /** Narrows the current view to messages with this person. */
    onFilterPerson?: (participantID: number, label: string) => void;
  }

  let { value, client = undefined, onOpenPerson = undefined, onFilterPerson = undefined }: Props = $props();

  const parsed = $derived(parseAddress(value));
  let notice = $state('');
  // One lookup per address: a pending lookup is shared by repeat clicks, a
  // settled answer (a person, or definitely none) is kept, and a failed
  // request is forgotten so the next click retries.
  let lookupFor = '';
  let lookup: Promise<number | undefined> | undefined;

  function participantID(): Promise<number | undefined> {
    const address = parsed.address;
    if (!client) return Promise.resolve(undefined);
    if (lookup && lookupFor === address) return lookup;
    lookupFor = address;
    const pending = resolveParticipantID(client, address);
    lookup = pending;
    pending.catch(() => {
      if (lookup === pending) {
        lookup = undefined;
        lookupFor = '';
      }
    });
    return pending;
  }

  async function withPerson(action: (id: number) => void): Promise<void> {
    notice = '';
    let id: number | undefined;
    try {
      id = await participantID();
    } catch {
      notice = `Could not look up ${parsed.address}. Try again.`;
      return;
    }
    if (id === undefined) {
      notice = `No person found for ${parsed.address}.`;
      return;
    }
    action(id);
  }

  async function copy(): Promise<void> {
    const copied = await copyToClipboard(parsed.address);
    notice = copied ? `Copied ${parsed.address}.` : 'Could not copy the address.';
  }
</script>

<span class="participant-pill">
  <Menu>
    <MenuTrigger class="participant-pill__trigger" ariaLabel={`${parsed.label}${parsed.name ? ` (${parsed.address})` : ''}: person actions`} title={parsed.address}>
      {parsed.label}
    </MenuTrigger>
    <MenuContent ariaLabel={`Actions for ${parsed.label}`}>
      <MenuItem onselect={() => void copy()}>Copy {parsed.address.includes('@') ? 'address' : 'number'}</MenuItem>
      {#if client && onOpenPerson}
        <MenuItem onselect={() => void withPerson((id) => onOpenPerson?.(id))}>Open person</MenuItem>
      {/if}
      {#if client && onFilterPerson}
        <MenuItem onselect={() => void withPerson((id) => onFilterPerson?.(id, parsed.label))}>Filter by person</MenuItem>
      {/if}
    </MenuContent>
  </Menu>
  {#if notice}<span class="kit-sr-only" role="status">{notice}</span>{/if}
</span>

<style>
  .participant-pill {
    display: inline-flex;
    max-width: 100%;
  }

  .participant-pill :global(.participant-pill__trigger) {
    max-width: 240px;
    overflow: hidden;
    padding: 0 var(--space-2);
    border: 1px solid var(--border-muted);
    border-radius: 999px;
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    line-height: 20px;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
