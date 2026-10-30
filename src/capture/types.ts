/** Capture is submitted evidence; role labels never establish authenticity. */
export type Result<T, E> = { ok: true; value: T } | { ok: false; error: E };
export type CaptureError = {
  code:
    | "invalid_input"
    | "policy_unavailable"
    | "paused"
    | "quota"
    | "storage_unavailable"
    | "busy"
    | "credential_unavailable"
    | "unauthorized"
    | "forbidden"
    | "stale"
    | "conflict"
    | "unavailable"
    | "cancelled";
};
export type Json =
  | null
  | boolean
  | number
  | string
  | Json[]
  | { [key: string]: Json };
export type CaptureEvent = {
  id: string;
  idempotencyKey: string;
  scope: { projectID: string; taskID?: string; sessionID?: string };
  kind: "message" | "tool_call" | "tool_result" | "checkpoint" | "artifact";
  observedAt: string;
  sequence: number;
  sourceLocator: string;
  sourcePath?: string;
  content: Json;
};
export type CapturePolicy = {
  revision: string;
  paused: boolean;
  excludedPaths: readonly string[];
  excludedFields: readonly string[];
  allowedModelRoutes: readonly string[];
  spoolLimitBytes: number;
  /** Collector freshness only; never a server-issued expiration. */ expiresAt: number;
  localExcludedPaths?: readonly string[];
  localExcludedFields?: readonly string[];
};
export type FilteredEvent = CaptureEvent & { policyRevision: string };
export type GapReason =
  | "quota"
  | "paused"
  | "excluded"
  | "policy_changed"
  | "policy_unavailable"
  | "invalid_input";
export type Gap = { sequence: number; reason: GapReason };
export type FilterOutcome =
  | { kind: "accepted"; event: FilteredEvent }
  | { kind: "excluded" | "withheld"; gap: Gap };
export type Receipt = {
  id: string;
  state: "queued" | "withheld" | "excluded" | "dropped" | "duplicate";
};
export type SpoolBatch = {
  events: FilteredEvent[];
  /** Count of all queued events, not only those returned in this batch. */
  queued: number;
  gaps: Gap[];
  totalLosses: number;
  paused: boolean;
};
export type CaptureStatus = {
  paused: boolean;
  queued: number;
  gaps: Gap[];
  totalLosses: number;
};
export type FlushReport = { sent: number; gaps: Gap[]; totalLosses: number };
export interface CaptureSpool {
  withDispatch<T>(
    operation: () => Promise<Result<T, CaptureError>>,
  ): Promise<Result<T, CaptureError>>;
  appendFiltered(
    event: FilteredEvent,
    limitBytes?: number,
  ): Promise<Result<Receipt, CaptureError>>;
  readBatch(): Promise<Result<SpoolBatch, CaptureError>>;
  acknowledge(id: string): Promise<Result<void, CaptureError>>;
  recordGap(
    sequence: number,
    reason: GapReason,
  ): Promise<Result<void, CaptureError>>;
  setPaused(paused: boolean): Promise<Result<void, CaptureError>>;
  discard(id: string, reason: GapReason): Promise<Result<void, CaptureError>>;
}
/** Implementations must call guard immediately before EVERY outbound payload request. */
export interface CaptureUploadGateway {
  policy(
    projectID: string,
    signal?: AbortSignal,
  ): Promise<Result<CapturePolicy, CaptureError>>;
  send(
    event: FilteredEvent,
    signal: AbortSignal,
    guard: () => Promise<Result<void, CaptureError>>,
  ): Promise<Result<void, CaptureError>>;
}
export const MAX_EVENT_BYTES = 32 * 1024 * 1024;
export const MAX_CHUNK_BYTES = 8 * 1024 * 1024;
export const DEFAULT_SPOOL_BYTES = 256 * 1024 * 1024;
/** Upper bound accepted for an explicitly configured local spool. */
export const MAX_SPOOL_BYTES = 16 * 1024 * 1024 * 1024;
/** Collector-local freshness of a fetched policy. The server remains the only
 * authority: an expired or missing policy withholds sending and keeps the queue. */
export const POLICY_FRESHNESS_MS = 60_000;
export interface CredentialStore {
  get(profile: string): Promise<Result<string, CaptureError>>;
  set(profile: string, bearer: string): Promise<Result<void, CaptureError>>;
}
