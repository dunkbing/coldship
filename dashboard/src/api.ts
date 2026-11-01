// Typed client for the Coldship HTTP API. Types mirror the Go JSON shapes.

export type Stage = 'production' | 'staging' | 'preview'
export type Scope = 'read' | 'deploy' | 'admin'

export interface Volume {
  name: string
  path: string
}

export interface Build {
  git: string
  ref?: string
  commit?: string
  context?: string
  dockerfile?: string
  args?: Record<string, string>
  builder?: '' | 'dockerfile' | 'auto'
  auto?: { language?: string; version?: string; install?: string; build?: string; start?: string }
}

export interface Spec {
  name: string
  stage: Stage
  image?: string
  build?: Build
  port?: number
  host_port?: number
  domains?: string[]
  databases?: { name: string; engine: 'postgres' | 'mysql' | 'redis'; version?: string; env?: string }[]
  uses?: string[]
  on_push?: OnPush
  health?: { path?: string; timeout?: string }
  memory?: string
  env?: Record<string, string>
  volumes?: Volume[]
}

export interface App {
  name: string
  spec?: Spec
  current_deployment?: string
  preview_of?: string
  expires_at?: string
  created_at: string
  updated_at: string
}

export interface RuntimeStatus {
  exists: boolean
  state?: string
  running: boolean
  exit_code: number
  oom_killed: boolean
  restart_count: number
  started_at?: string
  image?: string
  error?: string
}

export interface Deployment {
  id: string
  app: string
  plan_id: string
  spec?: Spec
  status: 'building' | 'deploying' | 'running' | 'superseded' | 'failed' | 'deleted'
  image?: string
  commit?: string
  error?: string
  error_code?: string
  failed_in?: 'build' | 'deploy'
  kept_previous?: boolean
  strategy?: 'zero-downtime' | 'recreate'
  detected?: string
  actor: string
  reason: string
  created_at: string
  finished_at?: string
}

export interface OnPush {
  action: 'plan' | 'deploy'
  previews?: boolean
  preview_ttl?: string
}

export interface Delivery {
  id: string
  provider: string
  event: string
  ref?: string
  commit?: string
  pusher?: string
  message?: string
  result: string
  detail?: string
  plan?: string
  preview?: string
  created_at: string
}

export interface WebhookInfo {
  url: string
  secret?: string
  app: string
  enabled: boolean
  enabled_at?: string
  on_push?: OnPush
  ref?: string
  deliveries: Delivery[]
  note: string
}

export interface Snapshot {
  id: string
  app: string
  database: string
  engine: string
  version: string
  size: number
  reason: string
  actor: string
  created_at: string
}

export interface MetricPoint {
  t: string
  cpu_percent: number
  memory_bytes: number
  memory_limit_bytes: number
  net_in_bps: number
  net_out_bps: number
  disk_read_bps: number
  disk_write_bps: number
  requests_per_s: number
  errors_4xx_per_s: number
  errors_5xx_per_s: number
  avg_latency_ms: number
}

export interface AppMetrics {
  interval_seconds: number
  container: boolean
  http: boolean
  points: MetricPoint[]
}

export interface Usage {
  total: number
  used: number
}

export interface ServerStatus {
  version: string
  host: {
    hostname: string
    os: string
    kernel: string
    arch: string
    cpus: number
    cpu_used_percent: number
    load: [number, number, number]
    uptime_seconds: number
    memory: Usage
    swap: Usage
    disks: (Usage & { path: string; for: string })[]
  }
  engine?: { name: string; version: string; containers: number; running: number; images: number; storage_driver: string; root_dir: string; host: string }
  engine_error?: string
  proxy?: { enabled: boolean; running: boolean; http_port?: number; https_port?: number; error?: string }
  data_dir: string
  started_at: string
  apps: number
  data_services: number
}

export type DataEngine = 'postgres' | 'mysql' | 'redis' | 's3'

// A data service (app === undefined) or a database an app owns.
export interface DataView {
  name: string
  engine: DataEngine
  version: string
  app?: string
  host: string
  status: { exists: boolean; running: boolean; state?: string; image?: string }
  used_by: string[]
  env: string[]
  latest_snapshot?: Snapshot
  created_at: string
}

export interface DatabaseView {
  name: string
  engine: string
  version: string
  env: string
  host: string
  status: { exists: boolean; running: boolean; state?: string }
  snapshots: number
  last_snapshot?: Snapshot
}

export interface AppView extends App {
  runtime?: RuntimeStatus
  latest_deployment?: Deployment
  urls?: string[]
  databases?: DatabaseView[]
}

export interface Change {
  path: string
  op: 'create' | 'update' | 'delete' | 'redeploy'
  from?: unknown
  to?: unknown
  destructive?: boolean
  reason?: string
}

export interface Plan {
  id: string
  app: string
  action: string
  desired?: Spec
  base_deployment?: string
  changes: Change[]
  destructive: boolean
  downtime: boolean
  requires_approval: boolean
  approved_by?: string
  status: 'pending' | 'applied' | 'expired'
  created_by: string
  created_at: string
  expires_at: string
  applied_at?: string
  deployment?: string
  next_step: string
}

export interface Finding {
  severity: 'info' | 'warning' | 'error'
  code: string
  message: string
  suggested_fix?: unknown
}

export interface Diagnosis {
  app: string
  healthy: boolean
  findings: Finding[]
  runtime: RuntimeStatus
  spec?: Spec
  latest_deployment?: Deployment
  recent_logs: string[]
  logs_note: string
  deploy_log?: string[]
}

export interface DeployLog {
  deployment: string
  app: string
  commit?: string
  lines: string[]
  untrusted: boolean
  note: string
}

export interface LogsResult {
  app: string
  lines: string[]
  untrusted: boolean
  note: string
}

export interface AuditEntry {
  id: number
  time: string
  actor: string
  action: string
  target: string
  reason?: string
  detail?: Record<string, unknown>
}

// Patch changes only the fields it names; the server keeps the rest of the
// app, secrets included.
// Patch changes only the fields it names; the server keeps the rest.
export interface Patch {
  name: string
  image?: string
  build?: { git: string; ref?: string }
  stage?: Stage
  port?: number
  host_port?: number
  remove_host_port?: boolean
  memory?: string
  remove_memory?: boolean
  env?: Record<string, string>
  unset_env?: string[]
  add_domains?: string[]
  remove_domains?: string[]
  add_databases?: { engine: 'postgres' | 'mysql' | 'redis'; name?: string }[]
  add_uses?: string[]
  remove_uses?: string[]
}

export interface ApplyResult {
  plan: Plan
  deployment?: Deployment
  message: string
}

export interface WhoAmI {
  name: string
  scope: Scope
  // The apps this token is limited to; absent means every app.
  apps?: string[]
}

export interface SystemStatus {
  version: string
  enabled: boolean
  disabled_reason?: string
  latest?: string
  update_available: boolean
  notes?: string
  notes_url?: string
  published_at?: string
  auto_upgrade: boolean
  auto_blocked?: string
  last_checked?: string
  check_error?: string
  upgrade_state: 'idle' | 'waiting' | 'downloading' | 'installing' | 'restarting'
  upgrade_error?: string
}

export class ApiError extends Error {
  status: number
  code: string
  hint?: string
  field?: string
  suggestedFix?: unknown
  details?: Record<string, unknown>

  constructor(status: number, body: { code?: string; message?: string; hint?: string; field?: string; suggested_fix?: unknown; details?: Record<string, unknown> }) {
    super(body.message ?? `HTTP ${status}`)
    this.status = status
    this.code = body.code ?? 'http_error'
    this.hint = body.hint
    this.field = body.field
    this.suggestedFix = body.suggested_fix
    this.details = body.details
  }
}

// The dashboard signs in by exchanging an API token for an HttpOnly session
// cookie (POST /v1/sessions). The token itself is never stored in the browser.
// State-changing requests carry X-Coldship-Request, the server's CSRF check.
async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: {
      'X-Coldship-Request': '1',
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  const data = text ? JSON.parse(text) : null
  if (res.status === 401 && path !== '/v1/sessions') window.dispatchEvent(new Event('coldship:signed-out'))
  if (!res.ok) throw new ApiError(res.status, data?.error ?? {})
  return data as T
}

const enc = encodeURIComponent

export const api = {
  login: (token: string) => req<WhoAmI>('POST', '/v1/sessions', { token }),
  logout: () => req<{ signed_out: boolean }>('DELETE', '/v1/sessions'),
  whoami: () => req<WhoAmI>('GET', '/v1/whoami'),
  apps: () => req<App[]>('GET', '/v1/apps'),
  app: (name: string) => req<AppView>('GET', `/v1/apps/${enc(name)}`),
  deployments: (name: string, limit = 50) => req<Deployment[]>('GET', `/v1/apps/${enc(name)}/deployments?limit=${limit}`),
  deployLog: (deployment: string) => req<DeployLog>('GET', `/v1/deployments/${enc(deployment)}/log`),
  diagnose: (name: string) => req<Diagnosis>('GET', `/v1/apps/${enc(name)}/diagnose`),
  logs: (name: string, tail = 200) => req<LogsResult>('GET', `/v1/apps/${enc(name)}/logs?tail=${tail}`),
  rollback: (name: string, reason: string, toDeployment?: string) =>
    req<ApplyResult>('POST', `/v1/apps/${enc(name)}/rollback`, { reason, to_deployment: toDeployment || undefined }),
  plans: (status?: string, limit = 100) =>
    req<Plan[]>('GET', `/v1/plans?limit=${limit}${status ? `&status=${enc(status)}` : ''}`),
  approve: (id: string) => req<Plan>('POST', `/v1/plans/${enc(id)}/approve`),
  planPatch: (patch: Patch) => req<Plan>('POST', '/v1/plans', { patch }),
  planDelete: (name: string) => req<Plan>('POST', '/v1/plans', { delete: name }),
  plan: (id: string) => req<Plan>('GET', `/v1/plans/${enc(id)}`),
  apply: (id: string, reason: string) => req<ApplyResult>('POST', `/v1/plans/${enc(id)}/apply`, { reason }),
  snapshots: (app: string, database = '') => req<Snapshot[]>('GET', `/v1/apps/${enc(app)}/snapshots?database=${enc(database)}`),
  snapshot: (app: string, database: string, reason = 'manual, from the dashboard') =>
    req<Snapshot>('POST', `/v1/apps/${enc(app)}/databases/${enc(database)}/snapshots`, { reason }),
  webhook: (app: string) => req<WebhookInfo>('GET', `/v1/apps/${enc(app)}/webhook`),
  enableWebhook: (app: string, rotate = false) => req<WebhookInfo>('POST', `/v1/apps/${enc(app)}/webhook`, { rotate }),
  disableWebhook: (app: string) => req<{ disabled: boolean }>('DELETE', `/v1/apps/${enc(app)}/webhook`),
  previews: () => req<App[]>('GET', '/v1/previews'),
  deletePreview: (name: string, reason = 'deleted from the dashboard') =>
    req<ApplyResult>('DELETE', `/v1/previews/${enc(name)}?reason=${enc(reason)}`),
  data: () => req<DataView[]>('GET', '/v1/data'),
  dataOne: (name: string) => req<DataView>('GET', `/v1/data/${enc(name)}`),
  createData: (engine: DataEngine, name: string, version?: string) =>
    req<DataView>('POST', '/v1/data', { engine, name, version: version || undefined }),
  deleteData: (name: string, reason = 'deleted from the dashboard') =>
    req<{ deleted: boolean }>('DELETE', `/v1/data/${enc(name)}?reason=${enc(reason)}`),
  dataConnection: (name: string) => req<Record<string, string>>('POST', `/v1/data/${enc(name)}/connection`),
  dataSnapshots: (name: string) => req<Snapshot[]>('GET', `/v1/data/${enc(name)}/snapshots`),
  snapshotData: (name: string, reason = 'manual, from the dashboard') =>
    req<Snapshot>('POST', `/v1/data/${enc(name)}/snapshots`, { reason }),
  restoreData: (name: string, snapshot: string, reason: string) =>
    req<{ message: string }>('POST', `/v1/data/${enc(name)}/restore`, { snapshot, reason }),
  system: () => req<SystemStatus>('GET', '/v1/system'),
  metrics: (name: string) => req<AppMetrics>('GET', `/v1/apps/${enc(name)}/metrics`),
  serverStatus: () => req<ServerStatus>('GET', '/v1/system/status'),
  upgrade: () => req<SystemStatus>('POST', '/v1/system/upgrade'),
  audit: (limit = 200) => req<AuditEntry[]>('GET', `/v1/audit?limit=${limit}`),
}
