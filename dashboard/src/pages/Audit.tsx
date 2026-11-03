import { useMemo, useState } from 'react'
import { api } from '../api'
import { useLoad } from '../hooks'
import { ScrollText } from 'lucide-react'
import { Empty, ErrorBox, Mono, PageHeader, TableSkeleton, Time } from '@/components/common'
import { Badge } from '@/components/ui/badge'
import { Card } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

// Radix Select can't use an empty value, so "all actors" gets a sentinel.
const ALL = '__all__'

export default function Audit() {
  const audit = useLoad(() => api.audit(500), [], 10000)
  const [actor, setActor] = useState('')
  const actors = useMemo(() => [...new Set(audit.data?.map((e) => e.actor) ?? [])].sort(), [audit.data])
  const rows = audit.data?.filter((e) => !actor || e.actor === actor) ?? []

  return (
    <>
      <PageHeader
        title="Audit log"
        description="Every change, by whom and why. Agents act under their own token names."
        actions={
          <div className="flex items-center gap-2">
            <Label htmlFor="audit-actor" className="text-muted-foreground">
              Actor
            </Label>
            <Select value={actor || ALL} onValueChange={(v) => setActor(v === ALL ? '' : v)}>
              <SelectTrigger id="audit-actor" className="min-w-36">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>All</SelectItem>
                {actors.map((a) => (
                  <SelectItem key={a} value={a}>
                    {a}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        }
      />
      {audit.error && <ErrorBox error={audit.error} />}
      {audit.loading && !audit.data && <TableSkeleton rows={6} cols={6} />}
      {audit.data && rows.length === 0 && (
        <Empty title="Nothing recorded yet" icon={<ScrollText />}>
          Every deploy, approval, sign-in and setting change shows up here: who did it, to what, and the reason they gave.
        </Empty>
      )}
      {rows.length > 0 && (
        // Narrow screens get a list: who did what to what, then why.
        <Card className="gap-0 divide-y py-0 xl:hidden">
          {rows.map((e) => (
            <div key={e.id} className="space-y-1 px-4 py-3 text-sm">
              <div className="flex items-baseline justify-between gap-3">
                <div className="min-w-0 break-words">
                  <span className="font-medium">{e.actor}</span> <Mono>{e.action}</Mono> {e.target}
                </div>
                <span className="shrink-0 text-xs text-muted-foreground">
                  <Time iso={e.time} />
                </span>
              </div>
              {e.reason && <div className="text-muted-foreground">{e.reason}</div>}
              {e.detail && Object.keys(e.detail).length > 0 && (
                <div className="flex flex-wrap gap-1">
                  {Object.entries(e.detail).map(([k, v]) => (
                    <Badge key={k} variant="secondary" className="h-auto max-w-full font-mono font-normal break-all whitespace-normal">
                      {k}: {String(v)}
                    </Badge>
                  ))}
                </div>
              )}
            </div>
          ))}
        </Card>
      )}
      {rows.length > 0 && (
        <Card className="hidden py-0 xl:flex">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead className="px-4">When</TableHead>
                <TableHead>Actor</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Target</TableHead>
                <TableHead>Reason</TableHead>
                <TableHead className="px-4">Detail</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((e) => (
                <TableRow key={e.id} className="align-top">
                  <TableCell className="px-4 text-muted-foreground">
                    <Time iso={e.time} />
                  </TableCell>
                  <TableCell className="font-medium">{e.actor}</TableCell>
                  <TableCell>
                    <Mono>{e.action}</Mono>
                  </TableCell>
                  <TableCell>{e.target}</TableCell>
                  <TableCell className="min-w-48 max-w-sm whitespace-normal">
                    {e.reason || <span className="text-muted-foreground">—</span>}
                  </TableCell>
                  <TableCell className="min-w-48 px-4 whitespace-normal">
                    <div className="flex max-w-md flex-wrap gap-1">
                      {e.detail &&
                        Object.entries(e.detail).map(([k, v]) => (
                          <Badge key={k} variant="secondary" className="h-auto max-w-full font-mono font-normal break-all whitespace-normal">
                            {k}: {String(v)}
                          </Badge>
                        ))}
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </>
  )
}
