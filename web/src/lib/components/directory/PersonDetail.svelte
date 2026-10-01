<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { Button, EmptyState, Menu, MenuContent, MenuItem, MenuTrigger, Notice } from '@kenn-io/kit-ui';
  import EllipsisIcon from '@lucide/svelte/icons/ellipsis';
  import { getRelationshipTimeline } from '../../api/generated/exploration/exploration';
  import type { Employment, MeetingRef, PersonIdentifier, TimelineRow } from '../../api/generated/models';
  import IdentityAvatar from '../common/IdentityAvatar.svelte';
  import LinkedValue from '../common/LinkedValue.svelte';
  import { mapLink } from '../../links/contact-links';
  import RecentActivity from '../people/RecentActivity.svelte';
  import MeetingPanel from '../meetings/MeetingPanel.svelte';
  import type { APIClient } from '../../api/client';
  import { entityNames } from '../../names/entity-names.svelte';
  import { resolveBoundClusters, type BoundClusterResolution } from '../../people/clusters';
  import { mergeReachEntries, reachEntriesFromContactPoints, reachEntriesFromIdentifiers } from '../../people/reach';
  import { humanizeDate, shortDate } from '../../util/dates';
  import { channelLabel } from '../../util/labels';
  import PersonContactList from './PersonContactList.svelte';
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
  import CorrespondentKindBanner from '../people/CorrespondentKindBanner.svelte';
  import NotAPersonDialog from '../people/NotAPersonDialog.svelte';
  import { clearKind, isNotAPerson, kindLabel, type NotAPersonKind } from '../../people/correspondent-kind';

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
    /** Opens a last-contact meeting's page (`meeting:<id>` refs). */
    onOpenMeetingPage?: (meetingID: number) => void;
    /** The open tab when the address names one (`/people/:id/<tab>`);
     * omitted, the page keeps its own tab. */
    tab?: PersonTab;
    onTabChange?: (tab: PersonTab) => void;
    /** Identity edits under Maintenance can change the person's bindings. */
    onReload?: () => void;
    /** The profile was deleted (after an explicit confirm) from here. */
    onDeleted?: () => void;
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
    onOpenMeetingPage = undefined,
    tab = undefined,
    onTabChange = undefined,
    onReload = undefined,
    onDeleted = undefined
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
  let resolutionAttempt = $state(0);
  $effect(() => {
    const ids: number[] = JSON.parse(participantKey);
    void resolutionAttempt;
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
  // Every lookup failed: say so instead of claiming nothing is linked,
  // which would invite linking the same identities again.
  const identitiesUnavailable = $derived(Boolean(
    participantResolution && participantResolution.clusters.length === 0 && participantResolution.failedIDs.length > 0));
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


  /** "Title · Organization · Location" for the current (primary first)
   * employment, when the employments projection has loaded one. */
  const subtitle = $derived.by((): string => {
    const employments = entityController?.employments ?? [];
    const current = employments.find((employment) => employment.is_current && employment.is_primary)
      ?? employments.find((employment) => employment.is_current);
    if (!current) return '';
    return [current.title ?? current.role, entityController?.organizationName(current.organization_id), current.location]
      .map((part) => part?.trim()).filter(Boolean).join(' · ');
  });

  const names = $derived(entityNames(client));
  const displayName = $derived(names.name('person', personID, bundle.person?.display_name || profile?.person?.display_name));

  /** `message:<id>` refs open the message and `meeting:<id>` refs the
   * meeting's page; other kinds render as plain text. */
  function contactRefTarget(ref: string | undefined): { kind: 'message' | 'meeting'; id: number } | undefined {
    const match = /^(message|meeting):([1-9]\d*)$/.exec(ref ?? '');
    return match ? { kind: match[1] as 'message' | 'meeting', id: Number(match[2]) } : undefined;
  }

  const lastContact = $derived.by(() => {
    const state = bundle.contactState;
    if (!state) return undefined;
    const channel = channelLabel(state.last_contact_channel);
    const parts: string[] = [];
    if (state.last_outbound_at) parts.push(`you wrote ${humanizeDate(state.last_outbound_at)}`);
    if (state.last_inbound_at) parts.push(`they wrote ${humanizeDate(state.last_inbound_at)}`);
    if (typeof state.interaction_count === 'number') {
      const since = state.first_contact_at ? ` since ${new Date(state.first_contact_at).getFullYear()}` : '';
      parts.push(`${state.interaction_count.toLocaleString()} interactions${since}`);
    }
    const cadence = state.cadence_status && state.cadence_status !== 'unknown' ? state.cadence_status.replaceAll('_', ' ') : '';
    if (cadence) parts.push(`cadence ${cadence}`);
    return {
      lead: state.last_contact_at ? `Last contact ${humanizeDate(state.last_contact_at)}${channel ? ` via ${channel}` : ''}` : 'No recorded contact',
      value: state.last_contact_at ? `${shortDate(state.last_contact_at)}${channel ? ` · ${channel}` : ''}` : 'No recorded contact',
      target: state.last_contact_at ? contactRefTarget(state.last_contact_ref) : undefined,
      rest: parts
    };
  });

  // Recent: the five newest items from the person's busiest archive
  // identity, the same interleaved stream the Timeline tab pages through.
  let recentRows = $state<TimelineRow[]>([]);
  let recentLoading = $state(false);
  let recentError = $state<string | null>(null);
  const recentClusterID = $derived(participantResolution?.clusters[0]?.canonicalID);
  $effect(() => {
    const id = recentClusterID;
    recentRows = [];
    recentError = null;
    if (id === undefined) return;
    const abort = new AbortController();
    recentLoading = true;
    void untrack(() => getRelationshipTimeline({ id }, {
      timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, limit: 5,
    }, { ...client, signal: abort.signal })).then(({ data }) => {
      if (abort.signal.aborted) return;
      if (data) recentRows = Array.isArray(data.rows) ? data.rows : [];
      else recentError = 'Recent activity is unavailable.';
    }).catch(() => {
      if (!abort.signal.aborted) recentError = 'Recent activity is unavailable.';
    }).finally(() => {
      if (!abort.signal.aborted) recentLoading = false;
    });
    return () => abort.abort();
  });
  const MEETING_KINDS = new Set(['calendar_event', 'meeting_transcript', 'meeting', 'event']);
  function openRecent(row: TimelineRow): void {
    const id = row.anchor_message_id;
    if (id === undefined) return;
    if (MEETING_KINDS.has(row.kind) && onOpenMeetingPage) onOpenMeetingPage(id);
    else onOpenMessage?.(id);
  }

  // Context: filled facts only.
  const currentEmployment = $derived.by((): Employment | undefined => {
    const employments = entityController?.employments ?? [];
    return employments.find((employment) => employment.is_current && employment.is_primary)
      ?? employments.find((employment) => employment.is_current);
  });
  const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const employmentText = $derived.by((): string => {
    const current = currentEmployment;
    if (!current) return '';
    const start = current.start_date?.year
      ? `since ${current.start_date.month ? `${MONTHS[current.start_date.month - 1]} ` : ''}${current.start_date.year}`
      : '';
    const organization = entityController?.organizationName(current.organization_id);
    return [organization, current.title ?? current.role, start].map((part) => part?.trim()).filter(Boolean).join(', ');
  });
  const locationFact = $derived.by((): { value: string; source: string } | undefined => {
    const address = profile?.addresses?.find((candidate) => candidate.original_value?.trim());
    if (address) {
      const value = [address.locality, address.region, address.country_name].filter(Boolean).join(', ') || address.original_value;
      return { value, source: address.envelope.source === 'user' ? '' : address.envelope.source.replaceAll('_', ' ') };
    }
    const location = currentEmployment?.location?.trim();
    return location ? { value: location, source: 'employment' } : undefined;
  });

  // "Not a person": every archive identity of this profile carries the same
  // kind, or none does. A partly classified profile shows no banner.
  const notAPerson = $derived.by(() => {
    const clusters = participantResolution?.clusters ?? [];
    const first = clusters[0]?.correspondentKind;
    if (!first || !isNotAPerson(first.kind)) return undefined;
    return clusters.every((cluster) => cluster.correspondentKind?.kind === first.kind) ? first : undefined;
  });
  let kindDialog = $state<{ initial?: NotAPersonKind }>();
  let kindPending = $state(false);
  let kindError = $state<string | null>(null);
  const clusterIDs = $derived((participantResolution?.clusters ?? []).map((cluster) => cluster.canonicalID));
  const suggestedOrganization = $derived.by((): string => {
    const email = participantIdentifiers.find((identifier) => identifier.type === 'email')?.value ?? '';
    return displayName.includes('@') && email.includes('@') ? email.slice(email.lastIndexOf('@') + 1) : displayName;
  });

  function kindDone(kind: string, deletedPersonID: number | undefined): void {
    kindDialog = undefined;
    if (deletedPersonID !== undefined) {
      onAnnounce(`Marked as ${kindLabel(kind).toLowerCase()} and deleted the saved profile.`);
      onDeleted?.();
      return;
    }
    onAnnounce(`Marked as ${kindLabel(kind).toLowerCase()}.`);
    resolutionAttempt += 1;
    onReload?.();
  }

  async function restorePerson(): Promise<void> {
    if (kindPending) return;
    kindPending = true;
    kindError = null;
    try {
      for (const id of clusterIDs) {
        const outcome = await clearKind(client, id);
        if (!outcome.ok) {
          kindError = outcome.message;
          return;
        }
      }
      onAnnounce('Marked as a person.');
      resolutionAttempt += 1;
      onReload?.();
    } finally {
      kindPending = false;
    }
  }

  // The header's overflow actions open the tab that holds each tool.
  let renameRequest = $state(0);
  let deleteRequest = $state(0);
  async function openTool(tab: DetailTab, request: 'rename' | 'delete' | '' = ''): Promise<void> {
    await selectTab(tab);
    if (request === 'rename') renameRequest += 1;
    if (request === 'delete') deleteRequest += 1;
  }

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

{#snippet identitiesFailed()}
  <div class="identities-failed" role="alert">
    <span>Could not load this person's archive identities. They may still be linked.</span>
    <Button size="sm" surface="soft" label="Retry" onclick={() => (resolutionAttempt += 1)} />
  </div>
{/snippet}

<section class="person-detail" aria-label="Person detail">
  {#if bundle.person || profile}
    <header class="person-header">
      <IdentityAvatar label={displayName} seed={`person:${personID}`} size={44} tone="accent" />
      <div class="person-heading">
        <h2 data-page-title class="person-name">{displayName}</h2>
        {#if subtitle}<p class="person-subtitle">{subtitle}</p>{/if}
      </div>
      <div class="person-actions">
        <Button size="sm" tone="info" surface="solid" label="Messages" ariaLabel={`Messages with ${displayName}`}
          onclick={() => void selectTab('timeline')} />
        <Button size="sm" surface="outline" label="Edit" ariaLabel={`Edit ${displayName}`}
          onclick={() => void selectTab('profile')} />
        <Menu align="end">
          <MenuTrigger class="person-more" ariaLabel={`More actions for ${displayName}`} title="More actions">
            <EllipsisIcon size={14} aria-hidden="true" />
          </MenuTrigger>
          <MenuContent ariaLabel={`More actions for ${displayName}`}>
            <MenuItem onselect={() => void openTool('profile', 'rename')}>Rename</MenuItem>
            <MenuItem onselect={() => void openTool('maintenance')}>Same person…</MenuItem>
            <MenuItem onselect={() => void openTool('maintenance')}>Merge or split…</MenuItem>
            <MenuItem onselect={() => void openTool('maintenance')}>Publish to CardDAV…</MenuItem>
            <MenuItem onselect={() => void openTool('maintenance')}>Track for profile maintenance…</MenuItem>
            <MenuItem disabled={clusterIDs.length === 0}
              onselect={() => (kindDialog = { initial: notAPerson?.kind as NotAPersonKind | undefined })}>Not a person…</MenuItem>
            <MenuItem tone="danger" onselect={() => void openTool('profile', 'delete')}>Delete…</MenuItem>
          </MenuContent>
        </Menu>
      </div>
    </header>
  {/if}
  {#if notAPerson}
    <CorrespondentKindBanner kind={notAPerson.kind as NotAPersonKind} organizationName={notAPerson.organization_name}
      pending={kindPending} error={kindError}
      onChange={() => (kindDialog = { initial: notAPerson?.kind as NotAPersonKind | undefined })}
      onRestore={() => void restorePerson()} />
  {/if}
  {#if kindDialog && clusterIDs.length > 0}
    <NotAPersonDialog
      {client}
      participantIDs={clusterIDs}
      label={displayName}
      suggestedOrganization={notAPerson?.organization_name ?? suggestedOrganization}
      initialKind={kindDialog.initial}
      onClose={() => (kindDialog = undefined)}
      onDone={(results, deleted) => kindDone(results[0]?.record.kind ?? 'ignored', deleted)}
    />
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

  <div id={panelID(activeTab)} role="tabpanel" aria-labelledby={tabID(activeTab)} tabindex="0" class:overview={activeTab === 'overview'}>
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
      {#if identitiesUnavailable}
        {@render identitiesFailed()}
      {:else if participantResolution || !(bundle.person?.participant_ids?.length)}
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
        <StructuredProfileSection {client} controller={profileController} {personID} {renameRequest} {deleteRequest} />
      {:else if profile?.names?.length}
        <section><h3 data-section-title>Names</h3><ul>{#each profile.names as name}<li>{nameText(name)} <small>{name.name_kind}</small></li>{/each}</ul></section>
      {/if}
      {#if !profileController && profile?.addresses?.length}
        <section><h3 data-section-title>Addresses</h3><ul>{#each profile.addresses as address}<li><LinkedValue link={mapLink(address)} text={address.original_value} /> <small>{address.address_kind}</small></li>{/each}</ul></section>
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
          {#if identitiesUnavailable}
            {@render identitiesFailed()}
          {:else if participantResolution || !(bundle.person?.participant_ids?.length)}
            <PersonClusterPanel {client} clusters={participantResolution?.clusters ?? []} mode="identities" {onAnnounce}
              onIdentitiesChanged={onReload} />
          {:else}
            <p class="state" role="status">Loading identities…</p>
          {/if}
        </section>
      </div>
    {:else}
      <PersonContactList {client} {personID} {displayName} entries={reachEntries}
        contactPoints={profile?.contact_points ?? []} revision={profile?.person?.revision ?? bundle.person?.revision}
        {profileController} {onAnnounce}>
        {#snippet after()}
          {#if lastContact}
            <li class="last-contact" data-fact-row>
              <span data-fact-label>Last contact</span>
              <span data-fact-value>
                {#if lastContact.target?.kind === 'message' && onOpenMessage}
                  {@const messageID = lastContact.target.id}
                  <button type="button" class="link-button" aria-label={lastContact.lead} onclick={() => onOpenMessage(messageID)}>{lastContact.value}</button>
                {:else if lastContact.target?.kind === 'meeting' && onOpenMeetingPage}
                  <a class="link-button" aria-label={lastContact.lead} href={`/meetings/${lastContact.target.id}`} onclick={(event) => {
                    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
                    event.preventDefault();
                    onOpenMeetingPage(lastContact.target!.id);
                  }}>{lastContact.value}</a>
                {:else}<span aria-label={lastContact.lead}>{lastContact.value}</span>{/if}
              </span>
              <span data-fact-meta>{lastContact.rest.join(' · ')}</span>
            </li>
          {/if}
        {/snippet}
      </PersonContactList>
      {#if bundle.person?.participant_ids?.length}
        <RecentActivity rows={recentRows} loading={recentLoading || (!participantResolution && !identitiesUnavailable)}
          error={identitiesUnavailable ? 'Recent activity is unavailable while identities fail to load.' : recentError}
          onOpen={openRecent} onSeeAll={() => void selectTab('timeline')} counterpart={displayName} />
      {/if}
      {#if locationFact || employmentText || (profileController?.attributes?.attributes ?? bundle.attributes?.attributes ?? []).some((group) => (group.current ?? []).length > 0)}
        <section class="context" aria-label="Context">
          <div data-section-line><h3 data-section-title>Context</h3><span>filled attributes only</span></div>
          <ul data-fact-list>
            {#if locationFact}
              <li data-fact-row><span data-fact-label>Location</span><span data-fact-value>{locationFact.value}</span>
                <span data-fact-meta>{locationFact.source}</span></li>
            {/if}
            {#if employmentText}
              <li data-fact-row><span data-fact-label>Employment</span><span data-fact-value>{employmentText}</span>
                <span data-fact-meta><button type="button" onclick={() => void selectTab('profile')}>History ({entityController?.employments.length ?? 0})</button></span></li>
            {/if}
          </ul>
          <AttributeSummary
            {client}
            groups={profileController?.attributes?.attributes ?? bundle.attributes?.attributes ?? []}
            onEdit={profileController ? () => void editAttributes() : undefined}
          />
        </section>
      {/if}
    {/if}
  </div>
</section>

<style>
  .person-detail { padding: var(--space-4); display: grid; gap: var(--space-5); }
  [role="tabpanel"] { display: grid; gap: var(--space-5); outline: none; }
  [role="tabpanel"].overview { gap: 0; }
  /* Text tabs: the selected one carries a 2px accent underline on the
   * strip's hairline; nothing is boxed. */
  .detail-tabs { display: flex; gap: var(--space-6); border-bottom: 1px solid var(--hairline); }
  [role="tab"] { margin-bottom: -1px; border: 0; border-bottom: 2px solid transparent; padding: 6px 0; background: transparent; color: var(--text-secondary); font: inherit; font-size: var(--font-size-sm); font-weight: 500; line-height: var(--leading-body); cursor: pointer; }
  [role="tab"]:hover { color: var(--text-primary); }
  [role="tab"][aria-selected="true"] { border-bottom-color: var(--accent-blue); color: var(--text-primary); }
  [role="tab"]:focus-visible { outline: var(--focus-ring); outline-offset: -2px; border-radius: var(--radius-sm); }
  section { display: grid; gap: var(--space-2); }
  h2, h3, p, ul { margin: 0; }
  small { color: var(--text-muted); font-size: var(--font-size-sm); }
  ul:not([data-fact-list]) { padding-left: var(--space-5); }
  .person-header { display: flex; flex-wrap: wrap; align-items: flex-start; gap: var(--space-5); }
  .person-heading { display: grid; min-width: 0; gap: 2px; }
  .person-name { font-size: 20px; line-height: 1.2; }
  .person-subtitle { color: var(--text-secondary); font-size: 13px; }
  .person-actions { display: flex; align-self: center; align-items: center; gap: var(--space-2); margin-left: auto; }
  .person-actions :global(.person-more) {
    display: inline-flex; align-items: center; justify-content: center; height: 26px; padding: 0 8px;
    border: 1px solid var(--edge); border-radius: var(--radius-md); background: var(--surface-panel);
    color: var(--text-secondary); cursor: pointer;
  }
  .person-actions :global(.person-more:hover) { color: var(--text-primary); }
  .context { display: grid; gap: 0; }
  .last-contact .link-button { text-decoration: none; }
  .last-contact .link-button:hover { text-decoration: underline; }
  .link-button { border: 0; padding: 0; background: none; color: inherit; font: inherit; text-decoration: underline; cursor: pointer; }
  .link-button:focus-visible { outline: var(--focus-ring); outline-offset: 2px; }
  .maintenance-body { display: grid; gap: var(--space-6); }
  .profile-group { display: grid; gap: var(--space-3); padding-top: var(--space-4); border-top: 1px solid var(--hairline); }
  .state { color: var(--text-muted); font-size: var(--font-size-sm); }
  .identities-failed { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3); color: var(--text-secondary); font-size: var(--font-size-sm); }
</style>
