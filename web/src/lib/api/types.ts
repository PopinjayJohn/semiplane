// API surface types for Phase 1/F (wiki read). Wire shapes mirror openapi.yaml.

export interface Identity {
  user_id: string;
  role: 'gm' | 'player';
}

export interface NoteEntry {
  path: string;
  title: string;
}

export interface NoteDoc {
  path: string;
  content: string;
  fragment: string;
  meta: Record<string, unknown>;
  /* Opaque ids (gm-0, gm-1, …) of the note's :::gm blocks, in source order.
   * Ids only — no block content, so it is present for players too (whose
   * content/fragment have the blocks stripped). Used for reveal-pending
   * affordances and to agree on block ids with GM reveal events. Absent
   * when the note has no :::gm blocks. */
  gm_blocks?: string[];
  backlinks: string[];
  etag: string;
}

export interface Problem {
  type: string;
  title: string;
  status: number;
  detail?: string;
}

export interface ListNotesResponse {
  notes: NoteEntry[];
}

export interface SearchHit {
  path: string;
  snippet: string;
  score: number;
}

export interface SearchResponse {
  hits: SearchHit[];
}

export interface GraphNode {
  path: string;
  title: string;
}

export interface GraphEdge {
  src: string;
  dst: string;
}

export interface GraphResponse {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

export interface RollResult {
  formula: string;
  total: number;
  faces: number[];
  seed: number;
  kept?: number[];
  discarded?: number[];
  breakdown?: string[];
}

export interface RevealResponse {
  ok: boolean;
  eventId: number;
}

export interface LoginBody {
  username: string;
  password: string;
}
