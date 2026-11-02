import { siMysql, siPostgresql, siRedis, siRustfs } from 'simple-icons'
import type { DataEngine } from '../api'

// What each kind of data service is, for pickers and lists. Versions match
// the server's supported versions; the last is the default.
export const dataKinds: Record<
  DataEngine,
  { label: string; blurb: string; versions: string[]; gives: string }
> = {
  postgres: {
    label: 'Postgres',
    blurb: 'Relational database. The default choice for most apps.',
    versions: ['14', '15', '16', '17'],
    gives: 'DATABASE_URL',
  },
  mysql: {
    label: 'MySQL',
    blurb: 'Relational database, for apps built on MySQL.',
    versions: ['8.4'],
    gives: 'DATABASE_URL',
  },
  redis: {
    label: 'Redis',
    blurb: 'In-memory store for caches, queues and sessions.',
    versions: ['6', '7'],
    gives: 'REDIS_URL',
  },
  s3: {
    label: 'S3 storage',
    blurb: 'S3-compatible object storage (SeaweedFS) for uploads and files, with a bucket of the same name.',
    versions: ['4.51'],
    gives: 'AWS_* and S3_* variables',
  },
  rustfs: {
    label: 'RustFS storage',
    blurb: 'S3-compatible object storage on RustFS, for uploads and files, with a bucket of the same name.',
    versions: ['1.0.4'],
    gives: 'AWS_* and S3_* variables',
  },
}

// S3-compatible storage: a data service only, not snapshotted.
export const isStorage = (e: string) => e === 's3' || e === 'rustfs'

// Real logos (Simple Icons, CC0); SeaweedFS storage gets a plain bucket,
// since it isn't Amazon S3.
const logos: Record<DataEngine, { path: string; hex: string }> = {
  postgres: siPostgresql,
  mysql: siMysql,
  redis: siRedis,
  s3: {
    hex: 'C77B12',
    path: 'M4 5.5C4 4.1 7.6 3 12 3s8 1.1 8 2.5v.2l-1.8 13.1C18 20.1 15.3 21 12 21s-6-.9-6.2-2.2L4 5.7v-.2zm2.1 1.9 1.6 11.2c.4.4 2 .9 4.3.9s3.9-.5 4.3-.9l1.6-11.2C16.3 8 14.2 8.3 12 8.3S7.7 8 6.1 7.4zM12 4.5c-3.5 0-6 .7-6.4 1 .4.3 2.9 1 6.4 1s6-.7 6.4-1c-.4-.3-2.9-1-6.4-1z',
  },
  rustfs: siRustfs,
}

export function DataMark({ engine, size = 28 }: { engine: DataEngine; size?: number }) {
  const logo = logos[engine]
  return (
    <span
      className="inline-flex shrink-0 items-center justify-center rounded-md border bg-white dark:bg-card"
      style={{ width: size, height: size }}
      aria-hidden
    >
      {logo && (
        <svg viewBox="0 0 24 24" width={size * 0.66} height={size * 0.66}>
          <path d={logo.path} fill={`#${logo.hex}`} />
        </svg>
      )}
    </span>
  )
}

export function envSummary(env: string[]): string {
  if (env.some((e) => e.startsWith('AWS_'))) return 'AWS_* and S3_* variables'
  return env.join(', ') || '—'
}
