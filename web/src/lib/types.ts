// Mirrors of the Go DTOs in internal/store/dtos.go. All timestamps are unix ms UTC.

export interface Project {
  id: string;
  name: string;
  color: string;
  archived: boolean;
  created_at: number;
  updated_at: number;
  deleted_at: number | null;
  server_seq?: number;
}

export interface TimeEntry {
  id: string;
  project_id: string | null;
  description: string;
  // Optional on purpose: rows stored before tags shipped, and rows pulled from a
  // server that has not been migrated yet, carry no such key. Declaring it required
  // would satisfy the type checker and then throw on the first render.
  tags?: string[];
  started_at: number;
  stopped_at: number | null;
  created_at: number;
  updated_at: number;
  deleted_at: number | null;
  server_seq?: number;
  // Server-owned, and optional for the same reason as tags: rows written before
  // agent tracking shipped, and rows from an unmigrated server, carry no key.
  // Present only on rows an agent session produced.
  agent_session_id?: string | null;
  // Server-owned: idle time inside the interval that must not be billed. Rows
  // written before it existed simply have none.
  paused_ms?: number;
}

export type TimeOffKind = "sick" | "vacation" | "dayoff";

export interface TimeOff {
  id: string;
  kind: TimeOffKind;
  date_from: string;
  date_to: string;
  note: string;
  created_at: number;
  updated_at: number;
  deleted_at: number | null;
  server_seq?: number;
}

export interface SyncChanges {
  projects?: Project[];
  time_entries?: TimeEntry[];
  time_off?: TimeOff[];
}

export interface SyncResponse {
  seq: number;
  changes: SyncChanges;
}

export interface User {
  id: string;
  email: string;
  name: string;
  picture_url: string;
}

export type TableName = "projects" | "time_entries" | "time_off";
export type SyncedRow = Project | TimeEntry | TimeOff;
