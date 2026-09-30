<script lang="ts">
  import { CopyButton } from '@kenn-io/kit-ui';

  import { contactLink, type ContactLink, type ContactLinkInput } from '../../links/contact-links';

  interface Props {
    /** The contact value to link. Undefined renders plain text. */
    input?: ContactLinkInput;
    /** A precomputed link (a map link) used instead of `input`. */
    link?: ContactLink;
    /** Visible text; defaults to the input value. */
    text?: string;
    /** Text the copy button writes. Defaults to the visible text; false hides the button. */
    copy?: string | false;
    /** Accessible name for the copy button. */
    copyLabel?: string;
    /** False inside a clickable row: render text only, never a nested
     * link or button. */
    interactive?: boolean;
    title?: string;
    class?: string;
  }

  let {
    input = undefined, link = undefined, text = undefined, copy = undefined, copyLabel = undefined,
    interactive = true, title = undefined, class: className = ''
  }: Props = $props();

  const visible = $derived(text ?? input?.value ?? '');
  const resolved = $derived(link ?? (input ? contactLink(input) : undefined));
  const copyText = $derived(copy === false ? undefined : copy ?? visible);
</script>

<!-- Markup is kept free of inter-tag whitespace so the value's text content
     is exactly the value. -->
<span class={['linked-value', className]} {title}>{#if interactive && resolved}<a
      class="linked-value__link"
      href={resolved.href}
      target={resolved.external ? '_blank' : undefined}
      rel={resolved.external ? 'noopener noreferrer' : undefined}
      title={resolved.label}
    >{visible}{#if resolved.external}{' '}<span class="kit-sr-only">(opens in new tab)</span>{/if}</a>{:else}<span
      class="linked-value__text">{visible}</span>{/if}{#if interactive && copyText}<CopyButton
      text={copyText} ariaLabel={copyLabel ?? `Copy ${visible}`} copiedAriaLabel={`Copied ${visible}`} revealOnHover
    />{/if}</span>

<style>
  .linked-value {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    min-width: 0;
    max-width: 100%;
  }

  .linked-value__link,
  .linked-value__text {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .linked-value__link {
    color: inherit;
    text-decoration: underline;
    text-decoration-color: var(--border-muted);
    text-underline-offset: 2px;
  }

  .linked-value__link:hover,
  .linked-value__link:focus-visible {
    color: var(--link-ink);
    text-decoration-color: currentColor;
  }

  .linked-value:hover :global(.kit-copy-btn--reveal),
  .linked-value:focus-within :global(.kit-copy-btn--reveal) {
    opacity: 1;
  }
</style>
