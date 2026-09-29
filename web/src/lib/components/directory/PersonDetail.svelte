<script lang="ts">
  import { untrack } from 'svelte';
  import { Button, EmptyState, Notice } from '@kenn-io/kit-ui';
  import type { MeetingRef, PersonIdentifier } from '../../api/generated/models';
  import MeetingPanel from '../meetings/MeetingPanel.svelte';
  import type { APIClient } from '../../api/client';
  import { resolveBoundClusters, type BoundClusterResolution } from '../../people/clusters';
  import { mergeReachEntries, reachEntriesFromContactPoints, reachEntriesFromIdentifiers } from '../../people/reach';
  import { humanizeDate } from '../../util/dates';
  import { channelLabel } from '../../util/labels';
  import PersonReachBlock from '../people/PersonReachBlock.svelte';
  import type { DirectoryReadBundle, DirectoryReadSection } from '../../directory/models';
  import type { DirectoryProfileController } from '../../directory/profile-controller.svelte';
  import type { DirectoryEntityController } from '../../directory/entity-controller.svelte';
  import FilesWorkspace from '../files/FilesWorkspace.svelte';
  import AttributeSection from './AttributeSection.svelte';
  import AttributeSummary from './AttributeSummary.svelte';
  import StructuredProfileSection from './StructuredProfileSection.svelte';
  import OrganizationEmploymentTab from './OrganizationEmploymentTab.svelte';
  import PersonNetwork from './PersonNetwork.svelte';
  import PersonMergeHistory from './PersonMergeHistory.svelte';
  import RelationshipsTab from './RelationshipsTab.svelte';
  import PersonTrackingControl from './PersonTrackingControl.svelte';
  import PersonBriefCard from './PersonBriefCard.svelte';
  import PersonAgenda from './PersonAgenda.svelte';
  import CardDAVPublicationControl from './CardDAVPublicationControl.svelte';
  import type { PersonSplitCommittedContext } from '../../directory/person-merge-history-controller.svelte';

  interface Props {
    client: APIClient;
    bundle: DirectoryReadBundle;
    personID: number;
    profileController?: DirectoryProfileController | null;
    entityController?: DirectoryEntityController | null;
    onOpenPerson?: (personID: number) => void;
    onSplitCommitted?: (context: PersonSplitCommittedContext) => void | Promise<void>;
    onOpenCardDAVConflict?: (conflictID: number) => void;
    onOpenCardDAVSettings?: () => void;
    onAnnounce?: (message: string) => void;
    onOpenMeeting?: (meeting: MeetingRef) => void;
    /** Opens the Relationships hub for this person's archive participants —
     * the inverse of the hub's "Contact record" action. The cluster
     * resolution this page already made for the reach block rides along
     * (once it has settled) so the shell need not repeat the lookups. */
    onOpenTimeline?: (participantIDs: number[], resolution?: BoundClusterResolution) => void;
    /** Opens the last-contact message in the Everything reading pane. */
    onOpenMessage?: (messageID: number) => void;
  }

  type DetailTab = 'overview' | 'organizations' | 'relationships' | 'network' | 'media';
  const tabOrder: DetailTab[] = ['overview', 'organizations', 'relationships', 'network', 'media'];
  let {
    client,
    bundle,
    personID,
    profileController = null,
    entityController = null,
    onOpenPerson = () => undefined,
    onSplitCommitted = () => undefined,
    onOpenCardDAVConflict = () => undefined,
    onOpenCardDAVSettings = () => undefined,
    onAnnounce = () => undefined,
    onOpenMeeting = undefined,
    onOpenTimeline = undefined,
    onOpenMessage = undefined
  }: Props = $props();
  let activeTab = $state<DetailTab>('overview');
  let organizationRequest = $state<{ id: number; key: number }>();
  let organizationRequestKey = 0;
  let overviewTab = $state<HTMLButtonElement>();
  let organizationsTab = $state<HTMLButtonElement>();
  let relationshipsTab = $state<HTMLButtonElement>();
  let networkTab = $state<HTMLButtonElement>();
  let mediaTab = $state<HTMLButtonElement>();
  const profile = $derived(profileController?.structuredProfile ?? bundle.structuredProfile);
  // Archive-observed identifiers for every participant bound to this person,
  // best-effort: the address book rows render without them. Bindings resolve
  // in parallel and collapse to one identifier set per cluster.
  let participantIdentifiers = $state<PersonIdentifier[]>([]);
  let participantResolution = $state<BoundClusterResolution>();
  const participantKey = $derived(JSON.stringify([...(bundle.person?.participant_ids ?? [])].sort((a, b) => a - b)));
  $effect(() => {
    const ids: number[] = JSON.parse(participantKey);
    participantIdentifiers = [];
    participantResolution = undefined;
    if (ids.length === 0) return;
    const abort = new AbortController();
    void untrack(() => resolveBoundClusters(ids, client, abort.signal)).then((resolution) => {
      if (abort.signal.aborted) return;
      participantResolution = resolution;
      participantIdentifiers = resolution.clusters.flatMap((cluster) => cluster.identifiers);
    });
    return () => abort.abort();
  });
  const reachEntries = $derived(mergeReachEntries(
    reachEntriesFromContactPoints(profile?.contact_points),
    reachEntriesFromIdentifiers({ identifiers: participantIdentifiers })
  ));
  const overviewTabID = $derived(`person-${personID}-overview-tab`);
  const overviewPanelID = $derived(`person-${personID}-overview-panel`);
  const organizationsTabID = $derived(`person-${personID}-organizations-tab`);
  const organizationsPanelID = $derived(`person-${personID}-organizations-panel`);
  const relationshipsTabID = $derived(`person-${personID}-relationships-tab`);
  const relationshipsPanelID = $derived(`person-${personID}-relationships-panel`);
  const networkTabID = $derived(`person-${personID}-network-tab`);
  const networkPanelID = $derived(`person-${personID}-network-panel`);
  const mediaTabID = $derived(`person-${personID}-media-tab`);
  const mediaPanelID = $derived(`person-${personID}-media-panel`);
  const sectionNames: Record<DirectoryReadSection, string> = {
    person: 'Person', structuredProfile: 'Profile', attributes: 'Attributes', contactState: 'Contact state',
    activity: 'Activity', files: 'Files'
  };

  function valueText(value: unknown): string {
    if (typeof value === 'string' || typeof value === 'number') return String(value);
    return JSON.stringify(value);
  }

  function nameText(name: NonNullable<NonNullable<DirectoryReadBundle['structuredProfile']>['names']>[number]): string {
    return name.formatted ?? ([name.given_name, name.family_name].filter(Boolean).join(' ') || name.original_value);
  }

  function employmentOrganization(employmentID: number): string | undefined {
    const projection = entityController?.employmentProjection;
    return projection?.employment_id === employmentID ? projection.organization_name : undefined;
  }

  /** "Title · Organization · Location" for the current (primary first)
   * employment, when the employments projection has loaded one. */
  const subtitle = $derived.by((): string => {
    const employments = entityController?.employments ?? [];
    const current = employments.find((employment) => employment.is_current && employment.is_primary)
      ?? employments.find((employment) => employment.is_current);
    if (!current) return '';
    return [current.title ?? current.role, employmentOrganization(current.id), current.location]
      .map((part) => part?.trim()).filter(Boolean).join(' · ');
  });

  const displayName = $derived(bundle.person?.display_name ?? profile?.person?.display_name ?? `Person ${personID}`);

  /** `message:<id>` refs open in the Everything reading pane; other kinds
   * (meetings) have no in-app destination and render as plain text. */
  function contactRefMessageID(ref: string | undefined): number | undefined {
    const match = /^message:([1-9]\d*)$/.exec(ref ?? '');
    return match ? Number(match[1]) : undefined;
  }

  const lastContact = $derived.by(() => {
    const state = bundle.contactState;
    if (!state) return undefined;
    const channel = channelLabel(state.last_contact_channel);
    const parts: string[] = [];
    if (state.last_outbound_at) parts.push(`you wrote ${humanizeDate(state.last_outbound_at)}`);
    if (state.last_inbound_at) parts.push(`they wrote ${humanizeDate(state.last_inbound_at)}`);
    if (typeof state.interaction_count === 'number') parts.push(`${state.interaction_count.toLocaleString()} interactions`);
    const cadence = state.cadence_status && state.cadence_status !== 'unknown' ? state.cadence_status.replaceAll('_', ' ') : '';
    if (cadence) parts.push(`cadence ${cadence}`);
    return {
      lead: state.last_contact_at ? `Last contact ${humanizeDate(state.last_contact_at)}${channel ? ` via ${channel}` : ''}` : 'No recorded contact',
      messageID: state.last_contact_at ? contactRefMessageID(state.last_contact_ref) : undefined,
      rest: parts
    };
  });

  async function selectTab(tab: DetailTab, focus = false): Promise<void> {
    activeTab = tab;
    if (!focus) return;
    await Promise.resolve();
    ({ overview: overviewTab, organizations: organizationsTab, relationships: relationshipsTab, network: networkTab, media: mediaTab }[tab])?.focus();
  }

  function openOrganization(organizationID: number): void {
    organizationRequest = { id: organizationID, key: ++organizationRequestKey };
    void selectTab('organizations');
  }

  function handleTabKeydown(event: KeyboardEvent): void {
    let next: DetailTab | undefined;
    const index = tabOrder.indexOf(activeTab);
    if (event.key === 'ArrowRight') next = tabOrder[(index + 1) % tabOrder.length];
    else if (event.key === 'ArrowLeft') next = tabOrder[(index - 1 + tabOrder.length) % tabOrder.length];
    else if (event.key === 'Home') next = 'overview';
    else if (event.key === 'End') next = 'media';
    if (!next) return;
    event.preventDefault();
    void selectTab(next, true);
  }
</script>

<section class="person-detail" aria-label="Person detail">
  <div class="detail-tabs" role="tablist" aria-label="Person detail sections">
    <button bind:this={overviewTab} id={overviewTabID} type="button" role="tab"
      aria-selected={activeTab === 'overview'} aria-controls={overviewPanelID}
      tabindex={activeTab === 'overview' ? 0 : -1} onkeydown={handleTabKeydown}
      onclick={() => void selectTab('overview')}>Overview</button>
    <button bind:this={organizationsTab} id={organizationsTabID} type="button" role="tab"
      aria-selected={activeTab === 'organizations'} aria-controls={organizationsPanelID}
      tabindex={activeTab === 'organizations' ? 0 : -1} onkeydown={handleTabKeydown}
      onclick={() => void selectTab('organizations')}>Organizations</button>
    <button bind:this={relationshipsTab} id={relationshipsTabID} type="button" role="tab"
      aria-selected={activeTab === 'relationships'} aria-controls={relationshipsPanelID}
      tabindex={activeTab === 'relationships' ? 0 : -1} onkeydown={handleTabKeydown}
      onclick={() => void selectTab('relationships')}>Relationships</button>
    <button bind:this={networkTab} id={networkTabID} type="button" role="tab"
      aria-selected={activeTab === 'network'} aria-controls={networkPanelID}
      tabindex={activeTab === 'network' ? 0 : -1} onkeydown={handleTabKeydown}
      onclick={() => void selectTab('network')}>Network</button>
    <button bind:this={mediaTab} id={mediaTabID} type="button" role="tab"
      aria-selected={activeTab === 'media'} aria-controls={mediaPanelID}
      tabindex={activeTab === 'media' ? 0 : -1} onkeydown={handleTabKeydown}
      onclick={() => void selectTab('media')}>Media &amp; Files</button>
  </div>

  {#each Object.entries(bundle.errors).filter(([section]) => section !== 'files') as [section, message]}
    <Notice tone="error" message={`${sectionNames[section as DirectoryReadSection]}: ${message}`} />
  {/each}

  {#if activeTab === 'media'}
    <div id={mediaPanelID} role="tabpanel" aria-labelledby={mediaTabID} tabindex="0">
      {#if bundle.errors.files}
        <EmptyState title="No files to show" description={bundle.errors.files} />
      {:else}
        <!-- Durable Directory IDs use the People API, never the analytical participant route. -->
        <FilesWorkspace
          {client}
          identityScope={{ kind: 'durable-person', id: personID }}
          predicate={{ filters: [], presentation: 'files' }}
          sort={{ field: 'occurred_at', direction: 'desc' }}
          embedded
        />
      {/if}
    </div>
  {:else if activeTab === 'network'}
    <div id={networkPanelID} role="tabpanel" aria-labelledby={networkTabID} tabindex="0">
      {#if entityController}<PersonNetwork controller={entityController} {onOpenPerson} onOpenOrganization={openOrganization} />
      {:else}<section><h2 data-section-title>Network</h2><p>The curated network is unavailable for this selection.</p></section>{/if}
    </div>
  {:else if activeTab === 'relationships'}
    <div id={relationshipsPanelID} role="tabpanel" aria-labelledby={relationshipsTabID} tabindex="0">
      {#if entityController}<RelationshipsTab {client} controller={entityController} {personID} />
      {:else}<section><h2 data-section-title>Relationships</h2><p>Relationships are unavailable for this selection.</p></section>{/if}
    </div>
  {:else if activeTab === 'organizations'}
    <div id={organizationsPanelID} role="tabpanel" aria-labelledby={organizationsTabID} tabindex="0">
      {#if entityController}<OrganizationEmploymentTab controller={entityController} {personID} {organizationRequest} />
      {:else}<section><h2 data-section-title>Organizations</h2><p>Organizations are unavailable for this selection.</p></section>{/if}
    </div>
  {:else}
    <div id={overviewPanelID} role="tabpanel" aria-labelledby={overviewTabID} tabindex="0">
      {#if bundle.person || profile}
        <header class="person-header">
          <div class="person-title-row">
            <h2 data-page-title>{displayName}</h2>
            {#if onOpenTimeline && bundle.person?.participant_ids?.length}
              <Button
                label="Open timeline"
                ariaLabel={`Open timeline for ${displayName}`}
                surface="outline"
                size="sm"
                onclick={() => onOpenTimeline([...(bundle.person?.participant_ids ?? [])], participantResolution)}
              />
            {/if}
          </div>
          {#if subtitle}<p class="person-subtitle">{subtitle}</p>{/if}
        </header>
      {/if}
      <PersonReachBlock entries={reachEntries} {onAnnounce} />
      {#if lastContact}
        <p class="last-contact">
          {#if lastContact.messageID !== undefined && onOpenMessage}
            {@const messageID = lastContact.messageID}
            <button type="button" class="link-button" onclick={() => onOpenMessage(messageID)}>{lastContact.lead}</button>
          {:else}<span>{lastContact.lead}</span>{/if}
          {#each lastContact.rest as part}<span class="separator" aria-hidden="true">·</span><span>{part}</span>{/each}
        </p>
      {/if}
      <AttributeSummary
        groups={profileController?.attributes?.attributes ?? bundle.attributes?.attributes ?? []}
        onEdit={profileController ? () => {
          const section = document.getElementById('person-attributes');
          section?.scrollIntoView({ block: 'start', behavior: 'smooth' });
          section?.focus({ preventScroll: true });
        } : undefined}
      />
      {#if profileController}
        <StructuredProfileSection {client} controller={profileController} {personID} />
      {:else if profile?.names?.length}
        <section><h3 data-section-title>Names</h3><ul>{#each profile.names as name}<li>{nameText(name)} <small>{name.name_kind}</small></li>{/each}</ul></section>
      {/if}
      {#if !profileController && profile?.addresses?.length}
        <section><h3 data-section-title>Addresses</h3><ul>{#each profile.addresses as address}<li>{address.original_value} <small>{address.address_kind}</small></li>{/each}</ul></section>
      {/if}
      {#if !profileController && profile?.dates?.length}
        <section><h3 data-section-title>Dates</h3><ul>{#each profile.dates as date}<li>{date.label ?? date.date_kind}: {date.date_text ?? valueText(date.date)}</li>{/each}</ul></section>
      {/if}
      {#if !profileController && profile?.categories?.length}
        <section><h3 data-section-title>Categories</h3><ul>{#each profile.categories as category}<li>{category.original_value}</li>{/each}</ul></section>
      {/if}
      {#if profileController && profileController.attributes}
        <AttributeSection controller={profileController} />
      {/if}
      {#if entityController?.employments.length}
        <section><h3 data-section-title>Organizations and employment</h3><ul>{#each entityController.employments as employment}<li><span>{employment.title ?? employment.role ?? 'Employment'} · {employmentOrganization(employment.id) ?? `Organization ${employment.organization_id}`}</span>{#if employment.is_current}<small class="employment-flag">Current</small>{/if}</li>{/each}</ul></section>
      {/if}
      {#if entityController?.relationships.length}
        <section><h3 data-section-title>Relationships</h3><ul>{#each entityController.relationships as view}<li>{view.counterpart_display_name?.trim() || view.counterpart_vcard_uid || `Person ${view.counterpart_person_id}`} · {view.counterpart_label}</li>{/each}</ul></section>
      {/if}
      {#if bundle.person?.id === personID}
        <MeetingPanel {client} collapsible scope={{ kind: 'direct', scope: { person_id: personID } }}
          refreshKey={JSON.stringify([bundle.person.revision, [...bundle.person.participant_ids].sort((a, b) => a - b)])}
          {onOpenMeeting} />
      {/if}
      {#if bundle.activity}
        <section><h3 data-section-title>Activity</h3><p>{bundle.activity.total_count} recorded days</p></section>
      {/if}
      <details class="maintenance">
        <summary>Maintenance</summary>
        <div class="maintenance-body">
          <PersonAgenda {client} {personID} {onAnnounce} />
          <PersonTrackingControl {client} {personID} {onAnnounce} />
          <CardDAVPublicationControl
            {client}
            {personID}
            onOpenConflict={onOpenCardDAVConflict}
            onOpenSettings={onOpenCardDAVSettings}
            {onAnnounce}
          />
          <PersonBriefCard {client} {personID} {onAnnounce} />
          <PersonMergeHistory {client} {personID} {onOpenPerson} {onSplitCommitted} />
        </div>
      </details>
    </div>
  {/if}
</section>

<style>
  .person-detail { padding: var(--space-4); display: grid; gap: var(--space-5); }
  [role="tabpanel"] { display: grid; gap: var(--space-5); outline: none; }
  /* Text tabs: the selected one carries a 2px accent underline on the
   * strip's hairline; nothing is boxed. */
  .detail-tabs { display: flex; gap: var(--space-2); border-bottom: 1px solid var(--hairline); }
  [role="tab"] { margin-bottom: -1px; border: 0; border-bottom: 2px solid transparent; padding: var(--space-2) var(--space-3); background: transparent; color: var(--text-secondary); font: inherit; font-size: var(--font-size-sm); font-weight: 500; line-height: var(--leading-body); cursor: pointer; }
  [role="tab"]:hover { color: var(--text-primary); }
  [role="tab"][aria-selected="true"] { border-bottom-color: var(--accent-blue); color: var(--text-primary); }
  [role="tab"]:focus-visible { outline: var(--focus-ring); outline-offset: -2px; border-radius: var(--radius-sm); }
  section { display: grid; gap: var(--space-2); }
  h2, h3, p, ul { margin: 0; }
  small { color: var(--text-muted); font-size: var(--font-size-sm); }
  ul { padding-left: var(--space-5); }
  .person-header { display: grid; gap: var(--space-1); }
  .person-title-row { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--space-3); }
  .person-subtitle { color: var(--text-secondary); font-size: var(--font-size-sm); }
  .last-contact { display: flex; flex-wrap: wrap; gap: var(--space-2); color: var(--text-secondary); font-size: var(--font-size-sm); }
  .link-button { border: 0; padding: 0; background: none; color: inherit; font: inherit; text-decoration: underline; cursor: pointer; }
  .link-button:focus-visible { outline: var(--focus-ring); outline-offset: 2px; }
  .separator { color: var(--text-muted); }
  .employment-flag { margin-left: var(--space-2); }
  .maintenance summary { cursor: pointer; color: var(--text-secondary); font-size: var(--font-size-sm); }
  .maintenance-body { display: grid; gap: var(--space-6); margin-top: var(--space-4); }
</style>
