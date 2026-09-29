import type {
  RelationshipCalendarDay as GeneratedRelationshipCalendarDay,
  RelationshipCalendarHTTPResponse as GeneratedRelationshipCalendarHTTPResponse,
  RelationshipRow as GeneratedRelationshipRow,
  RelationshipSignals as GeneratedRelationshipSignals,
  TimelineRow as GeneratedTimelineRow,
} from '../api/generated/models';

export type RelationshipRow = GeneratedRelationshipRow;
export type RelationshipSignals = GeneratedRelationshipSignals;
export type RelationshipTimelineRow = GeneratedTimelineRow;
export type RelationshipCalendar = GeneratedRelationshipCalendarHTTPResponse;
export type RelationshipCalendarDay = GeneratedRelationshipCalendarDay;
export type RelationshipFacet = 'people' | 'domains';

/** One participant cluster a Directory person is bound to, for the hub's
 * "other identities" note when the bindings span several clusters. */
export interface RelationshipSiblingCluster {
  target: string;
  label: string;
  activityCount: number;
}
