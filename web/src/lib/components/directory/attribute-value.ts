import type { AttributeDefinition, AttributeValue } from '../../api/generated/models';
import type { EntityNames } from '../../names/entity-names.svelte';

/** Names a record reference; reactive when called from a template. */
function recordReference(value: AttributeValue, names: EntityNames): string {
  if (!value.record_id) return '—';
  if (value.record_type === 'person') return names.label('person', value.record_id);
  if (value.record_type === 'organization') return names.label('organization', value.record_id);
  return '—';
}

function rawValue(value: AttributeValue, names: EntityNames): string {
  switch (value.type) {
    case 'text':
      return value.text ?? '—';
    case 'integer':
      return value.integer?.toString() ?? '—';
    case 'real':
      return value.real?.toString() ?? '—';
    case 'boolean':
      return value.boolean === undefined ? '—' : value.boolean ? 'Yes' : 'No';
    case 'date':
      return value.date ?? '—';
    case 'timestamp':
      return value.timestamp ?? '—';
    case 'record_reference':
      return recordReference(value, names);
    default:
      return value.json === undefined ? '—' : JSON.stringify(value.json);
  }
}

/**
 * An attribute value as text. A record reference is named through the
 * resolver, so the text updates when the name arrives and never shows the ID.
 */
export function displayAttributeValue(definition: AttributeDefinition, value: AttributeValue, names: EntityNames): string {
  const display = rawValue(value, names);
  const canonical = value.type === 'boolean' && value.boolean !== undefined ? String(value.boolean) : display;
  const choice = definition.options?.choices?.find((candidate) => candidate.value === canonical);
  return choice?.label ?? display;
}
