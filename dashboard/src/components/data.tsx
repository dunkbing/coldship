import type { DataEngine } from '../api'

// What each kind of data service is, for pickers and lists. Versions match
// the server's supported versions; the last is the default.
export const dataKinds: Record<
  DataEngine,
  { label: string; blurb: string; versions: string[]; gives: string; color: string; mark: string }
> = {
  postgres: {
    label: 'Postgres',
    blurb: 'Relational database. The default choice for most apps.',
    versions: ['14', '15', '16', '17'],
    gives: 'DATABASE_URL',
    color: '#336791',
    mark: 'Pg',
  },
  mysql: {
    label: 'MySQL',
    blurb: 'Relational database, for apps built on MySQL.',
    versions: ['8.4'],
    gives: 'DATABASE_URL',
    color: '#00758f',
    mark: 'My',
  },
  redis: {
    label: 'Redis',
    blurb: 'In-memory store for caches, queues and sessions.',
    versions: ['6', '7'],
    gives: 'REDIS_URL',
    color: '#d82c20',
    mark: 'Rd',
  },
  s3: {
    label: 'S3 storage',
    blurb: 'S3-compatible object storage (SeaweedFS) for uploads and files, with a bucket of the same name.',
    versions: ['4.51'],
    gives: 'AWS_* and S3_* variables',
    color: '#c77b12',
    mark: 'S3',
  },
}

export function DataMark({ engine, size = 28 }: { engine: DataEngine; size?: number }) {
  const k = dataKinds[engine]
  return (
    <span className="data-mark" style={{ background: k?.color ?? 'var(--muted)', width: size, height: size }} aria-hidden>
      {k?.mark ?? '?'}
    </span>
  )
}

export function envSummary(env: string[]): string {
  if (env.some((e) => e.startsWith('AWS_'))) return 'AWS_* and S3_* variables'
  return env.join(', ') || '—'
}
