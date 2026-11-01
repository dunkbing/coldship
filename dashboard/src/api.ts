// Typed client for the Hangar HTTP API. Types mirror the Go JSON shapes.

export type Stage = 'production' | 'staging' | 'preview'
export type Scope = 'read' | 'deploy' | 'admin'

export interface Volume {
  name: string
  path: string
}

export interface Spec {
  name: string
  stage: Stage
  image: string
  port?: number
  host_port?: number
  memory?: string
  env?: Record<string, string>
  volumes?: Volume[]
}

export interface App {
  name: string
  spec?: Spec
  current_deployment?: string
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
  status: 'deploying' | 'running' | 'superseded' | 'failed' | 'deleted'
  error?: string
  actor: string
  reason: string
  created_at: string
  finished_at?: string
}

export interface AppView extends App {
  runtime?: RuntimeStatus
  latest_deployment?: Deployment
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

export interface ApplyResult {
  plan: Plan
  deployment?: Deployment
  message: string
}

export interface WhoAmI {
  name: string
  scope: Scope
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

const TOKEN_KEY = 'hangar.token'

export function getToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? ''
  } catch {
    return ''
  }
}

export function setToken(t: string) {
  try {
    localStorage.setItem(TOKEN_KEY, t)
  } catch {
    /* storage unavailable: token lives for this page only */
  }
  memoryToken = t
}

export function clearToken() {
  try {
    localStorage.removeItem(TOKEN_KEY)
  } catch {
    /* ignore */
  }
  memoryToken = ''
}

let memoryToken = getToken()

async function req<T>(method: string, path: string, body?: unknown, token = memoryToken): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  const data = text ? JSON.parse(text) : null
  if (!res.ok) throw new ApiError(res.status, data?.error ?? {})
  return data as T
}

const enc = encodeURIComponent

export const api = {
  whoami: (token?: string) => req<WhoAmI>('GET', '/v1/whoami', undefined, token),
  apps: () => req<App[]>('GET', '/v1/apps'),
  app: (name: string) => req<AppView>('GET', `/v1/apps/${enc(name)}`),
  deployments: (name: string, limit = 50) => req<Deployment[]>('GET', `/v1/apps/${enc(name)}/deployments?limit=${limit}`),
  diagnose: (name: string) => req<Diagnosis>('GET', `/v1/apps/${enc(name)}/diagnose`),
  logs: (name: string, tail = 200) => req<LogsResult>('GET', `/v1/apps/${enc(name)}/logs?tail=${tail}`),
  rollback: (name: string, reason: string, toDeployment?: string) =>
    req<ApplyResult>('POST', `/v1/apps/${enc(name)}/rollback`, { reason, to_deployment: toDeployment || undefined }),
  plans: (status?: string, limit = 100) =>
    req<Plan[]>('GET', `/v1/plans?limit=${limit}${status ? `&status=${enc(status)}` : ''}`),
  approve: (id: string) => req<Plan>('POST', `/v1/plans/${enc(id)}/approve`),
  audit: (limit = 200) => req<AuditEntry[]>('GET', `/v1/audit?limit=${limit}`),
}
