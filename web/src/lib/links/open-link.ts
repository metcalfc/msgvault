import type { ContactLink } from './contact-links';

/**
 * Follows a vetted contact link from a control that cannot itself be an
 * anchor, such as a menu item. It activates a transient anchor so the
 * browser applies the same rules as a clicked link: mailto:/tel: hand off
 * to the system handler in place, and web links open in a new tab with no
 * opener or referrer.
 */
export function openContactLink(link: ContactLink): void {
  const anchor = document.createElement('a');
  anchor.href = link.href;
  if (link.external) {
    anchor.target = '_blank';
    anchor.rel = 'noopener noreferrer';
  }
  anchor.hidden = true;
  document.body.append(anchor);
  try {
    anchor.click();
  } finally {
    anchor.remove();
  }
}
