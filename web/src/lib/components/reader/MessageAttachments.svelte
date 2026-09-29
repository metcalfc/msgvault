<script lang="ts">
  import FileArchiveIcon from '@lucide/svelte/icons/file-archive';
  import FileAudioIcon from '@lucide/svelte/icons/file-headphone';
  import FileIcon from '@lucide/svelte/icons/file';
  import FileTextIcon from '@lucide/svelte/icons/file-text';
  import FileVideoIcon from '@lucide/svelte/icons/file-play';
  import ImageIcon from '@lucide/svelte/icons/image';

  import type { ArchiveAttachment } from '../../archive/types';
  import type { FileViewerTarget } from '../../explore/models';

  interface Props {
    attachments: ArchiveAttachment[];
    messageId: number;
    conversationId?: number;
    /** Opens the file viewer; without it the list is informational. */
    onOpen?: (file: FileViewerTarget) => void;
  }

  let { attachments, messageId, conversationId = undefined, onOpen = undefined }: Props = $props();

  function glyph(mimeType: string) {
    const type = mimeType.toLowerCase();
    if (type.startsWith('image/')) return ImageIcon;
    if (type.startsWith('video/')) return FileVideoIcon;
    if (type.startsWith('audio/')) return FileAudioIcon;
    if (type === 'application/pdf' || type.startsWith('text/')) return FileTextIcon;
    if (/zip|tar|compressed|archive/.test(type)) return FileArchiveIcon;
    return FileIcon;
  }

  function bytes(value: number): string {
    if (value < 1024) return `${value} B`;
    if (value < 1024 * 1024) return `${Math.round(value / 1024)} KB`;
    return `${(value / (1024 * 1024)).toFixed(1)} MB`;
  }

  function target(attachment: ArchiveAttachment): FileViewerTarget | undefined {
    if (attachment.id === undefined) return undefined;
    return {
      id: attachment.id,
      message_id: messageId,
      conversation_id: conversationId,
      filename: attachment.filename,
      mime_type: attachment.mimeType,
      size_bytes: attachment.sizeBytes
    };
  }
</script>

<section class="message-attachments" aria-label={`${attachments.length} ${attachments.length === 1 ? 'attachment' : 'attachments'}`}>
  <ul>
    {#each attachments as attachment, index (`${attachment.id ?? index}:${attachment.filename}`)}
      {@const Glyph = glyph(attachment.mimeType)}
      {@const file = target(attachment)}
      <li>
        {#if onOpen && file}
          <button type="button" class="attachment kit-control-states" onclick={() => onOpen?.(file)}>
            <Glyph size={16} aria-hidden="true" />
            <span class="attachment__name">{attachment.filename || '(unnamed file)'}</span>
            <span class="attachment__size" data-mono>{bytes(attachment.sizeBytes)}</span>
          </button>
        {:else}
          <span class="attachment">
            <Glyph size={16} aria-hidden="true" />
            <span class="attachment__name">{attachment.filename || '(unnamed file)'}</span>
            <span class="attachment__size" data-mono>{bytes(attachment.sizeBytes)}</span>
          </span>
        {/if}
      </li>
    {/each}
  </ul>
</section>

<style>
  .message-attachments {
    padding: 0 var(--space-4) var(--space-4);
  }

  ul {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .attachment {
    display: inline-flex;
    max-width: 320px;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-1) var(--space-3);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-xs);
  }

  button.attachment {
    cursor: pointer;
  }

  .attachment :global(svg) {
    flex: none;
    color: var(--artifact-ink);
  }

  .attachment__name {
    min-width: 0;
    overflow: hidden;
    color: var(--text-primary);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .attachment__size {
    flex: none;
    color: var(--text-muted);
  }
</style>
