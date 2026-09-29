<script lang="ts">
  import { Button, Chip } from '@kenn-io/kit-ui';
  import { untrack } from 'svelte';

  import type {
    AttributeDefinition as GeneratedAttributeDefinition,
    PersonAttributeGroup as GeneratedPersonAttributeGroup,
    PersonAttributeValue as GeneratedPersonAttributeValue,
  } from '../../api/generated/models';
  import type { DirectoryProfileController } from '../../directory/profile-controller.svelte';
  import { stampText } from '../../util/dates';
  import AttributeDefinitionDialog from './AttributeDefinitionDialog.svelte';
  import AttributeEditor from './AttributeEditor.svelte';
  import { displayAttributeValue } from './attribute-value';

  type AttributeDefinition = GeneratedAttributeDefinition;
  type AttributeGroup = GeneratedPersonAttributeGroup;
  type PersonAttributeValue = GeneratedPersonAttributeValue;

  interface Props {
    controller: DirectoryProfileController;
  }

  interface JoinedAttribute {
    definition: AttributeDefinition;
    current: PersonAttributeValue[];
    history: PersonAttributeValue[];
  }

  let { controller }: Props = $props();
  let editing = $state<{ universalID: string; current?: PersonAttributeValue }>();
  let confirming = $state<{ universalID: string; current: PersonAttributeValue; position: number }>();
  let revealed = $state<Record<string, boolean>>({});
  let creatingDefinition = $state(false);
  let owner = untrack(() => controller);

  $effect(() => {
    if (controller === owner) return;
    owner = controller;
    editing = undefined;
    confirming = undefined;
    revealed = {};
    creatingDefinition = false;
    showEmpty = false;
    recentlyCreatedID = null;
  });

  const fields = $derived.by(() => joinDefinitions(controller.definitions, controller.attributes?.attributes ?? []));
  let showEmpty = $state(false);
  let recentlyCreatedID = $state<string | null>(null);
  const emptyCount = $derived(fields.filter((field) => field.current.length === 0).length);
  const visibleFields = $derived(showEmpty ? fields : fields.filter((field) =>
    field.current.length > 0 || editing?.universalID === field.definition.universal_id || recentlyCreatedID === field.definition.universal_id
  ));

  // Keep a newly created field actionable after its confirmation dialog closes.
  $effect(() => {
    if (controller.createdDefinition) recentlyCreatedID = controller.createdDefinition.universal_id;
  });

  function joinDefinitions(definitions: AttributeDefinition[], groups: AttributeGroup[]): JoinedAttribute[] {
    const byUniversalID = new Map(groups.map((group) => [group.definition.universal_id, group]));
    const joined = new Map<string, JoinedAttribute>();
    for (const definition of definitions) {
      const group = byUniversalID.get(definition.universal_id);
      joined.set(definition.universal_id, {
        definition,
        current: [...(group?.current ?? [])],
        history: historical(group?.history ?? []),
      });
    }
    for (const group of groups) {
      if (joined.has(group.definition.universal_id)) continue;
      joined.set(group.definition.universal_id, {
        definition: group.definition,
        current: [...(group.current ?? [])],
        history: historical(group.history ?? []),
      });
    }
    return [...joined.values()].sort(
      (left, right) =>
        left.definition.display_order - right.definition.display_order ||
        left.definition.slug.localeCompare(right.definition.slug),
    );
  }

  function historical(values: PersonAttributeValue[]): PersonAttributeValue[] {
    return values.filter((value) => value.active_until !== undefined || value.superseded_at !== undefined);
  }

  function isRevealed(definition: AttributeDefinition): boolean {
    return !definition.is_sensitive || revealed[definition.universal_id] === true;
  }

  function toggleReveal(definition: AttributeDefinition): void {
    const next = !revealed[definition.universal_id];
    revealed = { ...revealed, [definition.universal_id]: next };
    if (!next) discardEditor(definition);
  }

  function discardEditor(definition: AttributeDefinition): void {
    controller.discardAttributeDraft(definition.slug);
    if (editing?.universalID === definition.universal_id) editing = undefined;
    if (confirming?.universalID === definition.universal_id) confirming = undefined;
  }

  function provenance(value: PersonAttributeValue): string {
    return [
      `Source: ${value.source}`,
      value.actor ? `Actor: ${value.actor}` : undefined,
      value.source_ref ? `Reference: ${value.source_ref}` : undefined,
      value.confidence === undefined ? undefined : `Confidence: ${value.confidence}`,
      `Valid from: ${stampText(value.active_from)}`,
      value.active_until ? `Valid until: ${stampText(value.active_until)}` : undefined,
      value.superseded_at ? `Superseded: ${stampText(value.superseded_at)}` : undefined,
      `Created: ${stampText(value.created_at)}`,
    ]
      .filter(Boolean)
      .join(' · ');
  }

  function metadata(definition: AttributeDefinition): string {
    return [
      `Type: ${definition.value_type}`,
      `Field: ${definition.field_type}`,
      `Cardinality: ${definition.cardinality}`,
      `Ownership: ${definition.ownership}`,
    ].join(' · ');
  }

  function operationNeedsReload(definition: AttributeDefinition): boolean {
    const conflict = attributeConflict(definition);
    return conflict?.code === 'attribute_conflict' || conflict?.code === 'precondition_required';
  }

  function attributeConflict(definition: AttributeDefinition) {
    const draft = controller.draft;
    if (draft?.kind !== 'setAttribute' && draft?.kind !== 'clearAttribute') return null;
    return draft.slug === definition.slug ? controller.conflict : null;
  }

  function supported(definition: AttributeDefinition): boolean {
    if (definition.options?.choices?.length) {
      return ['text', 'integer', 'real', 'boolean', 'date', 'timestamp'].includes(definition.value_type);
    }
    return (
      definition.value_type === 'boolean' ||
      definition.value_type === 'integer' ||
      definition.value_type === 'real' ||
      definition.value_type === 'date' ||
      definition.value_type === 'timestamp' ||
      definition.value_type === 'json' ||
      definition.value_type === 'text' ||
      (definition.value_type === 'record_reference' && definition.record_target === 'person')
    );
  }

  function canAdd(field: JoinedAttribute): boolean {
    const definition = field.definition;
    return (
      definition.is_active &&
      definition.api_mutable &&
      definition.ui_creatable &&
      supported(definition) &&
      (!definition.is_sensitive || isRevealed(definition)) &&
      (definition.cardinality === 'multi' || field.current.length === 0) &&
      !controller.mutationPending &&
      !controller.reloadPending &&
      !controller.hasUnresolvedConflict &&
      !operationNeedsReload(definition)
    );
  }

  function canEdit(definition: AttributeDefinition): boolean {
    return (
      definition.is_active &&
      definition.api_mutable &&
      definition.ui_editable &&
      supported(definition) &&
      !controller.mutationPending &&
      !controller.reloadPending &&
      !controller.hasUnresolvedConflict &&
      !operationNeedsReload(definition)
    );
  }

  function canClose(definition: AttributeDefinition): boolean {
    // The store deliberately permits retiring a current value from an
    // inactive definition, but derived/non-API-mutable definitions remain
    // non-retractable.
    return (
      definition.api_mutable &&
      definition.ui_editable &&
      !controller.mutationPending &&
      !controller.reloadPending &&
      !controller.hasUnresolvedConflict &&
      !operationNeedsReload(definition)
    );
  }

  function openEditor(field: JoinedAttribute, current: PersonAttributeValue | undefined = undefined): void {
    if (current ? !canEdit(field.definition) : !canAdd(field)) return;
    editing = { universalID: field.definition.universal_id, ...(current ? { current } : {}) };
    confirming = undefined;
  }

  async function closeCurrent(): Promise<void> {
    if (!confirming) return;
    const field = fields.find((candidate) => candidate.definition.universal_id === confirming?.universalID);
    if (!field || !canClose(field.definition)) return;
    const pending = confirming;
    const result = await controller.clearAttribute(
      field.definition.slug,
      pending.current.id,
      field.definition.cardinality === 'multi' ? pending.current.ordinal : undefined,
    );
    if (result === undefined && controller.draft === null && controller.conflict === null) confirming = undefined;
  }

  async function reload(): Promise<void> {
    const result = await controller.reload();
    if (result.ok && controller.draft === null) confirming = undefined;
  }

  function canCreateDefinition(): boolean {
    return !controller.mutationPending && !controller.reloadPending && !controller.hasUnresolvedConflict;
  }
</script>

<section id="person-attributes" class="attribute-section" data-section aria-label="Attributes" tabindex="-1">
  <header data-section-header>
    <h3 data-section-title>Attributes</h3>
    <div data-section-actions>
      <Button
        label="Create attribute field"
        size="sm"
        surface="soft"
        disabled={!canCreateDefinition()}
        onclick={() => {
          creatingDefinition = true;
        }}
      />
    </div>
  </header>

  {#if creatingDefinition}
    <AttributeDefinitionDialog
      {controller}
      onClose={() => {
        creatingDefinition = false;
      }}
    />
  {/if}

  <div class="fields">
  {#each visibleFields as field (field.definition.universal_id)}
    <!-- One detail row per field: the definition on the left, its current
         values in the middle, the field's actions trailing; an editor,
         history, or conflict spans the row underneath. -->
    <section class="attribute-field" data-detail-row aria-labelledby={`attribute-title-${field.definition.id}`}>
      <div class="definition-copy" data-detail-label>
        <div class="title-row">
          <h4 id={`attribute-title-${field.definition.id}`} data-row-title>{field.definition.label}</h4>
          {#if field.definition.is_sensitive}<Chip tone="warning" size="xs" uppercase={false}>Sensitive</Chip>{/if}
        </div>
        {#if field.definition.description}<p>{field.definition.description}</p>{/if}
        <small>{metadata(field.definition)}</small>
        {#if field.definition.options?.choices?.length}
          <small>Allowed choices: {field.definition.options.choices.map((choice) => choice.label).join(', ')}</small>
        {/if}
        {#if field.definition.derived_source}<small>Computed by {field.definition.derived_source}</small>{/if}
      </div>

      <ul class="current-values" data-detail-value>
        {#each field.current as value, index (value.id)}
          <li>
            <div class="value-copy">
              {#if isRevealed(field.definition)}
                <strong>{displayAttributeValue(field.definition, value.value)}</strong>
              {:else}
                <strong>Sensitive value concealed.</strong>
              {/if}
              <small>{provenance(value)}</small>
            </div>
            <div class="value-actions" data-detail-actions="hover">
              {#if isRevealed(field.definition)}
                <Button
                  label={`Edit ${field.definition.label} value ${index + 1}`}
                  shortLabel="Edit"
                  size="sm"
                  surface="soft"
                  disabled={!canEdit(field.definition)}
                  onclick={() => openEditor(field, value)}
                />
                <Button
                  label={`Close ${field.definition.label} value ${index + 1}`}
                  shortLabel="Close"
                  size="sm"
                  tone="danger"
                  disabled={!canClose(field.definition)}
                  onclick={() => {
                    confirming = { universalID: field.definition.universal_id, current: value, position: index + 1 };
                    editing = undefined;
                  }}
                />
              {/if}
            </div>
            {#if confirming?.universalID === field.definition.universal_id && confirming.current.id === value.id}
              <div
                class="close-confirm"
                role="group"
                aria-label={`Confirm closing ${field.definition.label} value ${confirming.position}`}
              >
                <span>Close this current value while keeping it in history?</span>
                <Button
                  label="Cancel"
                  size="sm"
                  surface="soft"
                  onclick={() => {
                    confirming = undefined;
                  }}
                />
                <Button
                  label="Confirm close attribute"
                  size="sm"
                  tone="danger"
                  surface="solid"
                  disabled={!canClose(field.definition)}
                  onclick={() => void closeCurrent()}
                />
              </div>
            {/if}
          </li>
        {:else}
          <li class="empty">No current value.</li>
        {/each}
      </ul>

      <!-- A field with no value has nothing else to show, so its Add (and
           Reveal) actions stay visible; a field with values keeps them
           quiet until the row is hovered or focused. -->
      <div class="field-actions" data-detail-actions={field.current.length > 0 ? 'hover' : ''}>
        {#if field.definition.is_sensitive}
          <Button
            label={`${isRevealed(field.definition) ? 'Hide' : 'Reveal'} ${field.definition.label} values`}
            size="sm"
            surface="soft"
            onclick={() => toggleReveal(field.definition)}
          />
        {/if}
        <Button
          label={`Add ${field.definition.label} value`}
          size="sm"
          surface="soft"
          disabled={!canAdd(field)}
          onclick={() => openEditor(field)}
        />
      </div>

      {#if editing?.universalID === field.definition.universal_id}
        <div data-detail-below>
          {#key editing.current?.id ?? 'new'}
            <AttributeEditor
              {controller}
              definition={field.definition}
              current={editing.current}
              sensitiveRevealed={isRevealed(field.definition)}
              onDone={() => {
                editing = undefined;
              }}
              onCancel={() => discardEditor(field.definition)}
            />
          {/key}
        </div>
      {/if}

      {#if field.history.length}
        <details data-detail-below>
          <summary>History ({field.history.length})</summary>
          <ul class="history-values">
            {#each field.history as value (value.id)}
              <li>
                {#if isRevealed(field.definition)}
                  <strong>{displayAttributeValue(field.definition, value.value)}</strong>
                {:else}
                  <strong>Sensitive value concealed.</strong>
                {/if}
                <small>{provenance(value)}</small>
              </li>
            {/each}
          </ul>
        </details>
      {/if}

      {#if attributeConflict(field.definition) && editing?.universalID !== field.definition.universal_id}
        <div class="attribute-error" data-detail-below role="alert">
          <span
            >{controller.conflict?.code === 'attribute_conflict'
              ? 'This person changed elsewhere. Reload and retry.'
              : controller.conflict?.message}</span
          >
          {#if operationNeedsReload(field.definition)}
            <Button
              label="Reload attributes"
              size="sm"
              surface="soft"
              disabled={!controller.canReload}
              onclick={() => void reload()}
            />
          {/if}
        </div>
      {/if}
    </section>
  {:else}
    {#if fields.length === 0}<p class="empty">No attribute definitions are available.</p>{/if}
  {/each}
  </div>
  {#if emptyCount > 0}
    <div>
      <Button
        label={showEmpty ? 'Hide empty fields' : `Show empty fields (${emptyCount})`}
        surface="soft"
        size="sm"
        ariaExpanded={showEmpty}
        onclick={() => (showEmpty = !showEmpty)}
      />
    </div>
  {/if}
</section>

<style>
  .fields,
  .definition-copy,
  .value-copy {
    display: grid;
    gap: var(--space-1);
  }
  .fields {
    gap: 0;
  }
  .title-row,
  .close-confirm,
  .attribute-error {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    flex-wrap: wrap;
  }
  h3,
  h4,
  p,
  ul {
    margin: 0;
  }
  .definition-copy p {
    font-size: var(--font-size-sm);
  }
  small,
  .empty {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
  .current-values,
  .history-values {
    display: grid;
    gap: var(--space-2);
    padding: 0;
    list-style: none;
  }
  .current-values > li {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: start;
    gap: var(--space-1) var(--space-3);
  }
  .current-values strong {
    font-weight: 500;
  }
  .history-values > li {
    display: grid;
    gap: var(--space-1);
  }
  summary {
    cursor: pointer;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }
  .history-values {
    margin-top: var(--space-2);
  }
  .close-confirm,
  .attribute-error {
    grid-column: 1 / -1;
    padding: var(--space-2);
    border-radius: var(--radius-sm);
    background: var(--surface-well);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }
  .attribute-error {
    justify-content: space-between;
  }
</style>
