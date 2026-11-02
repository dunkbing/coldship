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
  jobs?: Job[]
  on_push?: OnPush
  health?: { path?: string; timeout?: string }
  memory?: string
  env?: Record<string, string>
  volumes?: Volume[]
}

export interface App {
  name: string
  org: string
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
  strategy?: 'zero-downtime' | 'recreate' | 'static'
  static?: boolean
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
  remote_key?: string
  remote_at?: string
  remote_error?: string
}

export interface BackupStatus {
  configured: boolean
  endpoint?: string
  region?: string
  bucket?: string
  prefix?: string
  access_key?: string
  secret_key_set?: boolean
  path_style?: boolean
  keep?: number
  public_only: boolean
  since?: string
  last_sync?: string
  last_error?: string
  pending: number
  uploaded: number
  interval?: string
}

export interface BackupConfig {
  endpoint: string
  region: string
  bucket: string
  prefix: string
  access_key: string
  secret_key: string
  path_style: boolean
  keep: number
}

export type ChannelKind = 'slack' | 'discord' | 'telegram' | 'webhook'

export interface Channel {
  id: string
  name: string
  kind: ChannelKind
  target: string
  events: string[]
  created_by: string
  created_at: string
  last_at?: string
  last_error?: string
  secret?: string
}

export interface ChannelRequest {
  name?: string
  kind?: ChannelKind
  url?: string
  bot_token?: string
  chat_id?: string
  events?: string[]
}

export interface EventKind {
  kind: string
  label: string
  default: boolean
  server?: boolean
}

export interface OffsiteBackup {
  key: string
  snapshot: string
  app?: string
  database: string
  engine: string
  version: string
  size: number
  sha256: string
  reason?: string
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

export interface GitHubStatus {
  connected: boolean
  account?: string
  since?: string
}

export interface GitHubRepo {
  full_name: string
  private: boolean
  default_branch: string
  clone_url: string
  description?: string
  updated_at: string
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

export type DataEngine = 'postgres' | 'mysql' | 'redis' | 's3' | 'rustfs'

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
  set_jobs?: Job[]
  remove_jobs?: string[]
}

export interface Job {
  name: string
  schedule: string
  command: string
  timeout?: string
}

export type JobStatus = 'running' | 'succeeded' | 'failed' | 'timed_out' | 'interrupted' | 'skipped'

export interface JobRun {
  id: string
  app: string
  job: string
  trigger: string
  command: string
  status: JobStatus
  exit_code?: number
  error?: string
  started_at: string
  finished_at?: string
  log?: string
}

export interface JobView extends Job {
  next?: string
  running?: string
  last?: JobRun
}

export interface ApplyResult {
  plan: Plan
  deployment?: Deployment
  message: string
}

export interface User {
  id: string
  email: string
  name: string
  created_at: string
}

export interface Org {
  id: string
  name: string
  created_at: string
  // The caller's role in it.
  role?: Scope
}

export interface OrgView extends Org {
  counts: { apps: number; data: number; members: number; tokens: number }
}

export interface Member extends User {
  role: Scope
  joined_at: string
}

export interface Invite {
  id: string
  kind: 'invite' | 'reset'
  role?: Scope
  created_by: string
  created_at: string
  expires_at: string
  // Only when just created: the link to share.
  url?: string
}

export interface LinkInfo {
  kind: 'invite' | 'reset'
  org?: string
  org_name?: string
  role?: Scope
  created_by?: string
  email?: string
  expires_at: string
}

export interface Token {
  name: string
  scope: Scope
  apps?: string[]
  org: string
  created_at: string
}

export interface AgentPreset {
  kind: string
  name: string
  cmd: string[]
  keys: string[]
  notes?: string
}

export interface Agent {
  name: string
  kind: string
  command?: string
  keys: string[]
  scope: Scope
  apps?: string[]
  triggers: string[]
  alert_fix: boolean
  created_by: string
  created_at: string
  running: number
}

export type SessionStatus = 'starting' | 'ready' | 'working' | 'ended' | 'failed'

export interface PermissionOption {
  optionId: string
  name: string
  kind: 'allow_once' | 'allow_always' | 'reject_once' | 'reject_always'
}

// One entry of an agent session's transcript. All text comes from the
// agent: render it as text, never HTML.
export interface SessionItem {
  key: string
  kind: 'user' | 'agent' | 'thought' | 'tool' | 'plan' | 'permission' | 'log' | 'notice'
  text?: string
  by?: string
  time: string
  tool?: { id: string; title: string; kind?: string; status: 'pending' | 'in_progress' | 'completed' | 'failed'; input?: string; output?: string }
  plan?: { content: string; priority?: string; status: 'pending' | 'in_progress' | 'completed' }[]
  permission?: {
    id: string
    title: string
    input?: string
    options: PermissionOption[]
    status: 'pending' | 'answered' | 'cancelled'
    chosen?: string
    answered_by?: string
  }
}

export interface AgentSession {
  id: string
  agent: string
  kind: string
  scope: Scope
  started_by: string
  created_at: string
  status: SessionStatus
  error?: string
  agent_info?: string
  alert?: string
  alert_app?: string
  title?: string
  updated_at?: string
  resumable: boolean
  modes?: { currentModeId: string; availableModes: { id: string; name: string; description?: string }[] }
  config_options?: AgentConfigOption[]
  commands?: AgentCommand[]
  usage?: { size: number; used: number; cost?: { amount: number; currency: string } }
  // The agent asks for permission and waits for an answer.
  waiting?: boolean
  items?: SessionItem[]
}

// Something on the overview that needs someone, worked out from how
// things are now. Title and detail may quote untrusted text.
export interface Attention {
  kind: 'app_failing' | 'deploy_failed' | 'job_failed' | 'approval' | 'agent_waiting' | 'data_stopped' | 'backup_failing' | 'disk_low' | 'update'
  severity: 'error' | 'warning' | 'info'
  title: string
  detail?: string
  link: string
  app?: string
  at?: string
}

export interface Overview {
  apps: AppView[]
  previews: number
  data: { total: number; running: number }
  deploys: Deployment[]
  attention: Attention[]
  jobs: number
  agents?: { configured: number; live: number; recent: AgentSession[] }
  server?: {
    hostname: string
    version: string
    load: [number, number, number]
    cpus: number
    memory: Usage
    disks: (Usage & { path: string; for: string })[]
    uptime_seconds: number
  }
}

// A setting the agent offers (ACP config option), like its model.
export interface AgentConfigOption {
  id: string
  name: string
  description?: string
  category?: string
  type: string
  currentValue: string | boolean
  options?: AgentConfigValue[]
}
export interface AgentConfigValue {
  value?: string
  name: string
  description?: string
  group?: string
  options?: AgentConfigValue[]
}
export interface AgentCommand {
  name: string
  description: string
  input?: { hint: string }
}

export interface SessionEvent {
  type: 'snapshot' | 'item' | 'status' | 'session'
  item?: SessionItem
  status?: SessionStatus
  error?: string
  session?: AgentSession
}

export interface WhoAmI {
  // Token name, or the signed-in person's email.
  name: string
  scope: Scope
  // The apps this token is limited to; absent means every app.
  apps?: string[]
  org?: Org
  // Set when a person (not a token) is signed in.
  user?: User
  orgs?: Org[]
  server_admin: boolean
  can_create_orgs: boolean
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
  if (res.status === 401 && path !== '/v1/sessions' && !path.startsWith('/v1/links/')) window.dispatchEvent(new Event('coldship:signed-out'))
  if (!res.ok) throw new ApiError(res.status, data?.error ?? {})
  return data as T
}

const enc = encodeURIComponent

export const api = {
  login: (token: string) => req<WhoAmI>('POST', '/v1/sessions', { token }),
  signIn: (email: string, password: string) => req<WhoAmI>('POST', '/v1/sessions', { email, password }),
  authInfo: () => req<{ has_users: boolean }>('GET', '/v1/auth'),
  createAccount: (email: string, name: string, password: string) => req<User>('POST', '/v1/account', { email, name, password }),
  updateAccount: (name: string) => req<User>('PATCH', '/v1/account', { name }),
  changePassword: (current: string, next: string) => req<{ changed: boolean }>('POST', '/v1/account/password', { current, new: next }),
  createOrg: (name: string) => req<Org>('POST', '/v1/orgs', { name }),
  switchOrg: (org: string) => req<WhoAmI>('POST', '/v1/session/org', { org }),
  org: () => req<OrgView>('GET', '/v1/org'),
  renameOrg: (name: string) => req<OrgView>('PATCH', '/v1/org', { name }),
  deleteOrg: () => req<{ deleted: string; switched_to: string }>('DELETE', '/v1/org'),
  members: () => req<Member[]>('GET', '/v1/org/members'),
  setRole: (id: string, role: Scope) => req<Member>('PATCH', `/v1/org/members/${enc(id)}`, { role }),
  removeMember: (id: string) => req<{ removed: string }>('DELETE', `/v1/org/members/${enc(id)}`),
  resetLink: (id: string) => req<Invite>('POST', `/v1/org/members/${enc(id)}/reset`),
  invites: () => req<Invite[]>('GET', '/v1/org/invites'),
  invite: (role: Scope) => req<Invite>('POST', '/v1/org/invites', { role }),
  revokeInvite: (id: string) => req<{ revoked: string }>('DELETE', `/v1/org/invites/${enc(id)}`),
  lookupLink: (secret: string) => req<LinkInfo>('POST', '/v1/links/lookup', { secret }),
  acceptLink: (secret: string, body: { email?: string; name?: string; password?: string }) =>
    req<WhoAmI>('POST', '/v1/links/accept', { secret, ...body }),
  moveApp: (name: string, org: string) => req<App>('POST', `/v1/apps/${enc(name)}/move`, { org }),
  tokens: () => req<Token[]>('GET', '/v1/tokens'),
  agents: () => req<{ presets: AgentPreset[]; agents: Agent[] }>('GET', '/v1/agents'),
  saveAgent: (name: string, body: { kind: string; command?: string; env?: Record<string, string>; scope?: Scope; apps?: string[]; triggers?: string[]; alert_fix?: boolean }) =>
    req<Agent>('PUT', `/v1/agents/${enc(name)}`, body),
  deleteAgent: (name: string) => req<{ deleted: boolean }>('DELETE', `/v1/agents/${enc(name)}`),
  startSession: (agent: string, prompt?: string) => req<AgentSession>('POST', `/v1/agents/${enc(agent)}/sessions`, prompt ? { prompt } : undefined),
  resumeSession: (id: string) => req<AgentSession>('POST', `/v1/agent-sessions/${enc(id)}/resume`),
  configureSession: (id: string, setting: { mode?: string; option?: string; value?: string }) =>
    req<AgentSession>('POST', `/v1/agent-sessions/${enc(id)}/settings`, setting),
  forgetSession: (id: string) => req<{ deleted: boolean }>('DELETE', `/v1/agent-sessions/${enc(id)}/transcript`),
  sessions: () => req<AgentSession[]>('GET', '/v1/agent-sessions'),
  session: (id: string) => req<AgentSession>('GET', `/v1/agent-sessions/${enc(id)}`),
  prompt: (id: string, text: string) => req<{ sent: boolean }>('POST', `/v1/agent-sessions/${enc(id)}/prompt`, { text }),
  answer: (id: string, perm: string, option: string) =>
    req<{ answered: boolean }>('POST', `/v1/agent-sessions/${enc(id)}/permissions/${enc(perm)}`, { option }),
  cancelTurn: (id: string) => req<{ cancelled: boolean }>('POST', `/v1/agent-sessions/${enc(id)}/cancel`),
  endSession: (id: string) => req<{ ended: boolean }>('DELETE', `/v1/agent-sessions/${enc(id)}`),
  revokeToken: (name: string, reason = 'revoked from the dashboard') =>
    req<{ revoked: boolean }>('DELETE', `/v1/tokens/${enc(name)}?reason=${enc(reason)}`),
  logout: () => req<{ signed_out: boolean }>('DELETE', '/v1/sessions'),
  whoami: () => req<WhoAmI>('GET', '/v1/whoami'),
  overview: () => req<Overview>('GET', '/v1/overview'),
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
  jobs: (app: string) => req<JobView[]>('GET', `/v1/apps/${enc(app)}/jobs`),
  runJob: (app: string, job: string) => req<JobRun>('POST', `/v1/apps/${enc(app)}/jobs/${enc(job)}/run`),
  jobRuns: (app: string, job = '') => req<JobRun[]>('GET', `/v1/apps/${enc(app)}/job-runs?job=${encodeURIComponent(job)}`),
  jobRun: (app: string, id: string) => req<JobRun>('GET', `/v1/apps/${enc(app)}/job-runs/${enc(id)}`),
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
  restoreDataBackup: (name: string, backup: string, reason: string) =>
    req<{ message: string }>('POST', `/v1/data/${enc(name)}/restore`, { backup, reason }),
  backupStatus: () => req<BackupStatus>('GET', '/v1/backups'),
  saveBackup: (cfg: BackupConfig) => req<BackupStatus>('PUT', '/v1/backups', cfg),
  testBackup: (cfg: BackupConfig) => req<{ ok: boolean }>('POST', '/v1/backups/test', cfg),
  deleteBackup: () => req<{ deleted: boolean }>('DELETE', '/v1/backups'),
  syncBackups: () => req<BackupStatus>('POST', '/v1/backups/sync'),
  backups: (database = '', app = '') =>
    req<OffsiteBackup[]>('GET', `/v1/backups/list?database=${enc(database)}&app=${enc(app)}`),
  channels: () => req<Channel[]>('GET', '/v1/channels'),
  channelEvents: () => req<EventKind[]>('GET', '/v1/channels/events'),
  createChannel: (r: ChannelRequest) => req<Channel>('POST', '/v1/channels', r),
  updateChannel: (id: string, r: ChannelRequest) => req<Channel>('PATCH', `/v1/channels/${enc(id)}`, r),
  deleteChannel: (id: string) => req<{ deleted: boolean }>('DELETE', `/v1/channels/${enc(id)}`),
  testChannel: (id: string) => req<Channel>('POST', `/v1/channels/${enc(id)}/test`),
  system: () => req<SystemStatus>('GET', '/v1/system'),
  metrics: (name: string) => req<AppMetrics>('GET', `/v1/apps/${enc(name)}/metrics`),
  github: () => req<GitHubStatus>('GET', '/v1/integrations/github'),
  connectGitHub: (token: string) => req<GitHubStatus>('PUT', '/v1/integrations/github', { token }),
  disconnectGitHub: () => req<{ connected: boolean }>('DELETE', '/v1/integrations/github'),
  githubRepos: () => req<GitHubRepo[]>('GET', '/v1/integrations/github/repos'),
  githubBranches: (fullName: string) => req<string[]>('GET', `/v1/integrations/github/repos/${fullName.split('/').map(enc).join('/')}/branches`),
  createToken: (name: string, scope: string, apps?: string[]) =>
    req<{ name: string; scope: string; apps?: string[]; token: string }>('POST', '/v1/tokens', { name, scope, apps }),
  serverStatus: () => req<ServerStatus>('GET', '/v1/system/status'),
  upgrade: () => req<SystemStatus>('POST', '/v1/system/upgrade'),
  audit: (limit = 200) => req<AuditEntry[]>('GET', `/v1/audit?limit=${limit}`),
}
