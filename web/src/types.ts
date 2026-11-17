export type Status = 'ok' | 'warn' | 'fail' | 'nodata' | 'disabled'
export type Role = 'admin' | 'viewer'
export type Level = 'playlist' | 'header' | 'segments' | 'container'

export interface User {
  id: number
  username: string
  role: Role
  disabled: boolean
  created_at: string
}

/** One way to watch a stream through the server. */
export interface WatchSource {
  kind: 'configured' | 'server'
  host?: string
  ip?: string
  software?: string
  /** The server answered for this stream lately. */
  seen: boolean
  last_seen?: string
  address: string
  url: string
}

/** A place a stream can be watched from. */
export interface WatchVantage {
  id: number
  name: string
  /** The server itself. */
  local: boolean
  available: boolean
  why?: 'offline' | 'too_old'
}

export interface WatchInfo {
  vantages: WatchVantage[]
  vantage: number
  sources: WatchSource[]
}

export interface AgentStatus {
  agent_id: number
  agent_name: string
  status: Status
  checked_at: string | null
  summary: Record<string, number>
}

export interface Stream {
  id: number
  name: string
  url: string
  enabled: boolean
  level: Level
  interval_sec: number
  timeout_sec: number
  max_segments: number
  headers: Record<string, string>
  insecure: boolean
  notes: string
  revision: number
  created_at: string
  updated_at: string
  agent_ids: number[]
  status: Status
  agents: AgentStatus[]
  open_incidents: Record<string, number>
}

export interface Hop {
  url: string
  status: number
  remote_addr?: string
  server?: string
}

/** The server that actually answered a request. */
export interface Endpoint {
  url: string
  host: string
  ip?: string
  proto?: string
  headers?: Record<string, string>
  redirects?: Hop[]
}

export interface VariantState {
  variant_id: number
  agent_id: number
  status: 'ok' | 'warn' | 'fail'
  checked_at: string
  metrics: Record<string, number>
  playlist_source?: Endpoint
  segment_source?: Endpoint
}

export interface Variant {
  id: number
  stream_id: number
  key: string
  kind: 'master' | 'media' | 'video' | 'audio' | 'subtitles'
  uri?: string
  name?: string
  bandwidth?: number
  avg_bandwidth?: number
  resolution?: string
  codecs?: string
  frame_rate?: number
  group?: string
  language?: string
  active: boolean
  last_seen: string
  states: VariantState[]
}

export interface StreamDetail extends Stream {
  variants: Variant[]
}

export interface Agent {
  id: number
  name: string
  kind: 'local' | 'remote'
  enabled: boolean
  enrolled: boolean
  enroll_expires?: string
  created_at: string
  last_seen: string | null
  version: string
  remote_addr: string
  clock_offset_ms: number | null
  online: boolean
  streams: number
  /** What the agent last told about itself; absent while it is offline. */
  status?: AgentStatus
}

export interface AgentStatus {
  queue_results: number
  queue_bytes: number
  queue_oldest_sec: number
  queue_dropped: number
  queue_failing?: boolean
  /** What the agent's host is short of, if anything. */
  overloaded?: string
  streams: number
  /** Streams checked later than they should be. */
  behind: number
  cpu_cores: number
  cpu_limit: number
  memory_bytes: number
}

export interface Enrollment {
  agent: Agent
  token: string
  expires_at: string
  server_url: string
  command: string
}

export interface Incident {
  id: number
  stream_id: number
  stream_name: string
  agent_id: number
  agent_name: string
  variant_id: number
  variant_key: string
  variant_kind: string
  kind: string
  severity: 'info' | 'warn' | 'crit'
  message: string
  started_at: string
  last_seen: string
  ended_at: string | null
  count: number
  /** The figures the incident was concluded from; absent for old incidents. */
  details?: Record<string, unknown>
}

/** Evidence of where an incident's cause is. */
export interface IncidentScope {
  agents: { id: number; name: string; checked: boolean; saw: boolean }[]
  vantage: 'all' | 'some' | 'only_this' | 'single'
  server?: string
  same_server: { id: number; name: string }[]
  elsewhere: number
  server_wide: boolean
}

export interface Overview {
  time: string
  counts: Record<string, number>
  streams: Stream[]
  agents: Agent[]
  incidents: Incident[]
}

export interface Timeline {
  from: number
  to: number
  step: number
  streams: Record<string, [number, number][]>
}

export interface Series {
  labels: Record<string, string>
  points: [number, number][]
}

export interface SeriesResponse {
  from: number
  to: number
  step: number
  metrics: { metric: string; series: Series[] }[]
}

export interface MediaTrack {
  id: number
  kind: string
  codec: string
  width?: number
  height?: number
  profile?: string
  level?: string
  fps?: number
  sample_rate?: number
  channels?: number
  frames: number
  bytes: number
  has_timing: boolean
  start_pts: number
  end_pts: number
  keyframes?: number
  first_keyframe?: boolean
  max_gop?: number
}

export interface MediaInfo {
  container: string
  tracks?: MediaTrack[]
  packets?: number
  sync_errors?: number
  cc_errors?: number
  transport_errors?: number
  has_pat?: boolean
  has_pmt?: boolean
  error?: string
}

export interface SegmentDetail {
  variant: string
  seq: number
  url: string
  time: string
  duration: number
  program_time?: string
  discontinuity?: boolean
  status: number
  error?: string
  remote_addr?: string
  proto?: string
  cache?: string
  size: number
  fetched?: number
  partial?: boolean
  dns_ms: number
  connect_ms: number
  tls_ms: number
  ttfb_ms: number
  total_ms: number
  bps: number
  source?: Endpoint
  media?: MediaInfo
  problems?: string[]
}

export interface SegmentRecord {
  id: number
  stream_id: number
  agent_id: number
  variant_id: number
  seq: number
  time: string
  url: string
  duration: number
  status: number
  error: string
  remote_addr: string
  size: number
  total_ms: number
  problems: string[]
  detail: SegmentDetail
}

export interface Snapshot {
  id: number
  stream_id: number
  agent_id: number
  variant_id: number
  time: string
  url: string
  problems: string[]
  body?: string
  /** The playlist at the check before, and when it was received. */
  previous_body?: string
  previous_time?: string | null
}

export interface ApiToken {
  id: number
  name: string
  created_at: string
  last_used_at: string | null
  token?: string
}

export interface SystemInfo {
  version: string
  time: string
  local_streams: number
  metrics: { enabled: boolean; queued_bytes: number; disk_bytes: number; dropped_batches: number }
  metrics_up: boolean
  public_url: string
  storage: {
    database_bytes: number
    journal_bytes: number
    journal_max_bytes: number
    journal_rows: number
    disk_free_bytes: number
    disk_total_bytes: number
  }
  load: { overloaded: boolean; reason?: string; cpu_cores: number; cpu_limit: number; memory_bytes: number; memory_limit_bytes: number }
  checks: { streams: number; running: number; behind: number; lag_p95_seconds: number; lag_max_seconds: number }
  results: { stored: number; repeated: number; failed: number }
}

export interface SourceSeen {
  stream_id: number
  variant_id: number
  agent_id: number
  what: 'playlist' | 'segment'
  host: string
  ip: string
  software: string
  first_seen: string
  last_seen: string
  hits: number
}

export interface ServerView {
  host: string
  ip: string
  software: string
  streams: number
  ok: number
  warn: number
  fail: number
  playlists: boolean
  segments: boolean
  last_seen: string
  failing_streams: { id: number; name: string }[]
  stream_ids: number[]
}

export interface ProbeProblem {
  kind: string
  severity: 'info' | 'warn' | 'crit'
  variant: string
  message: string
  count: number
  details?: Record<string, unknown>
}

/** What one check cycle found, as the checker reports it. */
export interface ProbeResult {
  target_id: number
  time: string
  status: 'ok' | 'warn' | 'fail'
  variants?: { key: string; kind: Variant['kind']; uri?: string; name?: string; bandwidth?: number; resolution?: string }[]
  sources?: { variant: string; playlist?: Endpoint; segment?: Endpoint }[]
  metrics: { n: string; v?: string; x: number }[]
  segments?: SegmentDetail[]
  problems?: ProbeProblem[]
  snapshots?: { variant: string; url: string; body: string; previous?: string; previous_time?: string }[]
}

/** One line of a live diagnosis. */
export interface DiagnoseEvent {
  type: 'start' | 'cycle' | 'end'
  time: string
  url?: string
  level?: Level
  seconds?: number
  n?: number
  took_ms?: number
  result?: ProbeResult
  reason?: string
}

export interface Aggregate {
  from: number
  to: number
  step: number
  series: Record<string, [number, number][]>
}
