export type ArchiveSearchMode = 'fts' | 'vector' | 'hybrid';

export type ArchiveFilterValue = string | number | boolean | string[];

export interface ArchiveFilter {
  field: string;
  operator: string;
  value: ArchiveFilterValue;
}

export interface ArchiveSort {
  field: string;
  direction: 'asc' | 'desc';
}

export type ArchivePresentation = 'table' | 'timeline' | 'files';

/**
 * Browser-restorable analytical context. Bulk selection is deliberately
 * excluded: selection can be large and is scoped to one live archive revision.
 */
export interface ArchiveURLState {
  schemaVersion: number;
  query: string;
  searchMode: ArchiveSearchMode;
  filters: ArchiveFilter[];
  groupingChain: string[];
  presentation: ArchivePresentation;
  sort: ArchiveSort[];
  columns: string[];
  selectedRow: string | null;
  inspectorPinned: boolean;
  conversationAnchor: string | null;
  scrollKey: string | null;
  bulkSelection?: never;
  [futureField: string]: unknown;
}

export interface ArchiveMessageSummary {
  id: number;
  conversationId: number;
  subject: string;
  sender: string;
  recipients: string[];
  sentAt: string;
  snippet: string;
}

export interface ArchiveAttachment {
  /** Attachment ID for the file viewer, when the daemon supplied one. */
  id?: number;
  filename: string;
  mimeType: string;
  sizeBytes: number;
}

export interface ArchiveMessageDetail extends ArchiveMessageSummary {
  body: string;
  bodyHtml?: string;
  /** Archive message type ("email", "calendar_event", …) when known. */
  messageType?: string;
  attachments: ArchiveAttachment[];
  /** The sender as "Name <address>" (or the bare address or phone). */
  from?: string;
  cc?: string[];
  isFromMe?: boolean;
  /** The provider's own message ID (a Gmail message's hex ID). */
  sourceMessageId?: string;
  /** A calendar event's stored provider links, unvetted: EventCard applies
   * the host allowlist before offering either one. */
  eventLinks?: { joinURL?: string; calendarURL?: string };
}

export type MessageViewMode = 'html' | 'text';
