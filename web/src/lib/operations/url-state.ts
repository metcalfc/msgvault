import type {
  ExploreURLState,
  OperationKind,
  OperationLane,
  OperationStatusAuthority,
  OperationState
} from '../explore/models';

type OperationURLState = Pick<
  ExploreURLState,
  | 'operationLane'
  | 'operationKind'
  | 'operationState'
  | 'operationStartedFrom'
  | 'operationStartedBefore'
  | 'operationRunID'
  | 'operationStatus'
>;

const OPERATION_LANES = new Set<OperationLane>([
  'messages',
  'person_facts',
  'contacts',
  'documents',
  'visual_attachments'
]);
const OPERATION_KINDS = new Set<OperationKind>([
  'carddav_sync',
  'document_embedding',
  'document_extraction',
  'message_embedding',
  'person_embedding',
  'person_enrichment',
  'person_sweep',
  'source_sync',
  'visual_embedding'
]);
const OPERATION_STATES = new Set<OperationState>(['cancelled', 'failed', 'partial', 'queued', 'running', 'succeeded']);
const OPERATION_STATUS_AUTHORITIES = new Set<OperationStatusAuthority>([
  'getDocumentIndexStatus',
  'getDocumentVectorStatus',
  'getVisualAttachmentStatus'
]);
const OPERATION_KINDS_BY_LANE: Record<OperationLane, ReadonlySet<OperationKind>> = {
  messages: new Set(['source_sync', 'message_embedding']),
  person_facts: new Set(['person_sweep', 'person_embedding', 'person_enrichment']),
  contacts: new Set(['carddav_sync']),
  documents: new Set(['document_extraction', 'document_embedding']),
  visual_attachments: new Set(['visual_embedding'])
};

function operationDateBound(value: unknown): string {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{0,8}[1-9])?Z$/.test(value)) return '';
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed)) return '';
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})/.exec(value);
  if (!match) return '';
  const [, year, month, day, hour, minute, second] = match;
  const date = new Date(parsed);
  return date.getUTCFullYear() === Number(year) &&
    date.getUTCMonth() + 1 === Number(month) &&
    date.getUTCDate() === Number(day) &&
    date.getUTCHours() === Number(hour) &&
    date.getUTCMinutes() === Number(minute) &&
    date.getUTCSeconds() === Number(second)
    ? value
    : '';
}

function operationDateSortKey(value: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?Z$/.exec(value);
  if (!match) return '';
  const [, year, month, day, hour, minute, second, fraction = ''] = match;
  return `${year}${month}${day}${hour}${minute}${second}${fraction.padEnd(9, '0')}`;
}

export function normalizeOperationRunID(value: unknown): string | null {
  return typeof value === 'string' && value.length <= 4096 && /^op2\.[a-f0-9]{32}\.[A-Za-z0-9_-]+$/.test(value)
    ? value
    : null;
}

/** Validate operation filters independently of browser history and other workspaces. */
export function normalizeOperationURLState(value: Record<string, unknown>): OperationURLState {
  const operationLane =
    typeof value.operationLane === 'string' && OPERATION_LANES.has(value.operationLane as OperationLane)
      ? (value.operationLane as OperationLane)
      : '';
  const candidateOperationKind =
    typeof value.operationKind === 'string' && OPERATION_KINDS.has(value.operationKind as OperationKind)
      ? (value.operationKind as OperationKind)
      : '';
  const operationKind =
    operationLane !== '' &&
    candidateOperationKind !== '' &&
    !OPERATION_KINDS_BY_LANE[operationLane].has(candidateOperationKind)
      ? ''
      : candidateOperationKind;
  const operationState =
    typeof value.operationState === 'string' && OPERATION_STATES.has(value.operationState as OperationState)
      ? (value.operationState as OperationState)
      : '';
  let operationStartedFrom = operationDateBound(value.operationStartedFrom);
  let operationStartedBefore = operationDateBound(value.operationStartedBefore);
  if (
    operationStartedFrom !== '' &&
    operationStartedBefore !== '' &&
    operationDateSortKey(operationStartedFrom) >= operationDateSortKey(operationStartedBefore)
  ) {
    operationStartedFrom = '';
    operationStartedBefore = '';
  }

  return {
    operationLane,
    operationKind,
    operationState,
    operationStartedFrom,
    operationStartedBefore,
    operationRunID: normalizeOperationRunID(value.operationRunID),
    operationStatus:
      typeof value.operationStatus === 'string' &&
      OPERATION_STATUS_AUTHORITIES.has(value.operationStatus as OperationStatusAuthority)
        ? (value.operationStatus as OperationStatusAuthority)
        : ''
  };
}
