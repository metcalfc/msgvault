<script lang="ts">
  import { tick, untrack } from 'svelte';
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
  import { PERSON_TABS, type PersonTab } from '../../routing/routes';
  import PersonClusterPanel from '../people/PersonClusterPanel.svelte';

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
    /** Opens the last-contact message. */
    onOpenMessage?: (messageID: number) => void;
    /** The open tab when the address names one (`/people/:id/<tab>`);
     * omitted, the page keeps its own tab. */
    tab?: PersonTab;
    onTabChange?: (tab: PersonTab) => void;
    /** Identity edits under Maintenance can change the person's bindings. */
    onReload?: () => void;
  }

  type DetailTab = PersonTab;
  const tabOrder: DetailTab[] = [...PERSON_TABS];
  const tabLabels: Record<DetailTab, string> = {
    overview: 'Overview', timeline: 'Timeline', files: 'Files', meetings: 'Meetings', profile: 'Profile', maintenance: 'Maintenance'
  };
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
    onOpenMessage = undefined,
    tab = undefined,
    onTabChange = undefined,
    onReload = undefined
  }: Props = $props();
  let ownTab = $state<DetailTab>('overview');
  const activeTab = $derived(tab ?? ownTab);
  let organizationRequest = $state<{ id: number; key: number }>();
  let organizationRequestKey = 0;
  const tabButtons: Partial<Record<DetailTab, HTMLButtonElement>> = $state({});
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
  const tabID = (value: DetailTab) => `person-${personID}-${value}-tab`;
  const panelID = (value: DetailTab) => `person-${personID}-${value}-panel`;
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

  /** `message:<id>` refs open the message; other kinds have no in-app
   * destination here and render as plain text. */
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

  async function selectTab(next: DetailTab, focus = false): Promise<void> {
    ownTab = next;
    if (next !== activeTab) onTabChange?.(next);
    if (!focus) return;
    await Promise.resolve();
    tabButtons[next]?.focus();
  }

  /** Edit attributes opens Profile and puts focus on its attributes. */
  async function editAttributes(): Promise<void> {
    await selectTab('profile');
    await tick();
    const section = document.getElementById('person-attributes');
    section?.scrollIntoView?.({ block: 'start', behavior: 'smooth' });
    section?.focus({ preventScroll: true });
  }

  function openOrganization(organizationID: number): void {
    organizationRequest = { id: organizationID, key: ++organizationRequestKey };
    void selectTab('profile');
  }

  function handleTabKeydown(event: KeyboardEvent): void {
    let next: DetailTab | undefined;
    const index = tabOrder.indexOf(activeTab);
    if (event.key === 'ArrowRight') next = tabOrder[(index + 1) % tabOrder.length];
    else if (event.key === 'ArrowLeft') next = tabOrder[(index - 1 + tabOrder.length) % tabOrder.length];
    else if (event.key === 'Home') next = 'overview';
    else if (event.key === 'End') next = tabOrder.at(-1);
    if (!next) return;
    event.preventDefault();
    void selectTab(next, true);
  }
</script>

<section class="person-detail" aria-label="Person detail">
  {#if bundle.person || profile}
    <header class="person-header">
      <h2 data-page-title>{displayName}</h2>
      {#if subtitle}<p class="person-subtitle">{subtitle}</p>{/if}
    </header>
  {/if}
  <div class="detail-tabs" role="tablist" aria-label="Person detail sections">
    {#each tabOrder as value (value)}
      <button bind:this={tabButtons[value]} id={tabID(value)} type="button" role="tab"
        aria-selected={activeTab === value} aria-controls={panelID(value)}
        tabindex={activeTab === value ? 0 : -1} onkeydown={handleTabKeydown}
        onclick={() => void selectTab(value)}>{tabLabels[value]}</button>
    {/each}
  </div>

  {#each Object.entries(bundle.errors).filter(([section]) => section !== 'files') as [section, message]}
    <Notice tone="error" message={`${sectionNames[section as DirectoryReadSection]}: ${message}`} />
  {/each}

  <div id={panelID(activeTab)} role="tabpanel" aria-labelledby={tabID(activeTab)} tabindex="0">
    {#if activeTab === 'files'}
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
    {:else if activeTab === 'timeline'}
      {#if participantResolution || !(bundle.person?.participant_ids?.length)}
        <PersonClusterPanel {client} clusters={participantResolution?.clusters ?? []} mode="timeline" {onOpenMeeting} />
      {:else}
        <p class="state" role="status">Loading timeline…</p>
      {/if}
    {:else if activeTab === 'meetings'}
      {#if bundle.person?.id === personID}
        <MeetingPanel {client} scope={{ kind: 'direct', scope: { person_id: personID } }}
          refreshKey={JSON.stringify([bundle.person.revision, [...bundle.person.participant_ids].sort((a, b) => a - b)])}
          {onOpenMeeting} />
      {:else}
        <p class="state">Meetings are unavailable for this person.</p>
      {/if}
    {:else if activeTab === 'profile'}
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
      {#if entityController}
        <section class="profile-group" aria-label="Organizations">
          <h3 data-section-title>Organizations</h3>
          <OrganizationEmploymentTab controller={entityController} {personID} {organizationRequest} />
        </section>
        <section class="profile-group" aria-label="Relationships">
          <h3 data-section-title>Relationships</h3>
          <RelationshipsTab {client} controller={entityController} {personID} />
        </section>
        <section class="profile-group" aria-label="Network">
          <h3 data-section-title>Network</h3>
          <PersonNetwork controller={entityController} {onOpenPerson} onOpenOrganization={openOrganization} />
        </section>
      {:else}
        <section><h3 data-section-title>Organizations, relationships, and network</h3><p>These are unavailable for this selection.</p></section>
      {/if}
    {:else if activeTab === 'maintenance'}
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
        <section class="profile-group" aria-label="Identities">
          <h3 data-section-title>Identities</h3>
          {#if participantResolution || !(bundle.person?.participant_ids?.length)}
            <PersonClusterPanel {client} clusters={participantResolution?.clusters ?? []} mode="identities" {onAnnounce}
              onIdentitiesChanged={onReload} />
          {:else}
            <p class="state" role="status">Loading identities…</p>
          {/if}
        </section>
      </div>
    {:else}
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
        onEdit={profileController ? () => void editAttributes() : undefined}
      />
      {#if entityController?.employments.length}
        <section><h3 data-section-title>Organizations and employment</h3><ul>{#each entityController.employments as employment}<li><span>{employment.title ?? employment.role ?? 'Employment'} · {employmentOrganization(employment.id) ?? `Organization ${employment.organization_id}`}</span>{#if employment.is_current}<small class="employment-flag">Current</small>{/if}</li>{/each}</ul></section>
      {/if}
      {#if entityController?.relationships.length}
        <section><h3 data-section-title>Relationships</h3><ul>{#each entityController.relationships as view}<li>{view.counterpart_display_name?.trim() || view.counterpart_vcard_uid || `Person ${view.counterpart_person_id}`} · {view.counterpart_label}</li>{/each}</ul></section>
      {/if}
      {#if bundle.activity}
        <section><h3 data-section-title>Activity</h3><p>{bundle.activity.total_count} recorded days</p></section>
      {/if}
    {/if}
  </div>
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
  .person-subtitle { color: var(--text-secondary); font-size: var(--font-size-sm); }
  .last-contact { display: flex; flex-wrap: wrap; gap: var(--space-2); color: var(--text-secondary); font-size: var(--font-size-sm); }
  .link-button { border: 0; padding: 0; background: none; color: inherit; font: inherit; text-decoration: underline; cursor: pointer; }
  .link-button:focus-visible { outline: var(--focus-ring); outline-offset: 2px; }
  .separator { color: var(--text-muted); }
  .employment-flag { margin-left: var(--space-2); }
  .maintenance-body { display: grid; gap: var(--space-6); }
  .profile-group { display: grid; gap: var(--space-3); padding-top: var(--space-4); border-top: 1px solid var(--hairline); }
  .state { color: var(--text-muted); font-size: var(--font-size-sm); }
</style>
