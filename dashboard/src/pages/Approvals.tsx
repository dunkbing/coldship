import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type Plan } from '../api'
import { useSession } from '../App'
import { useLoad } from '../hooks'
import { CheckCircle2, Loader2, ShieldCheck } from 'lucide-react'
import { cn } from '@/lib/utils'
import { CardsSkeleton, ChangeList, Empty, ErrorBox, Mono, PageHeader, PlanMeta, ToneBadge } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter, CardHeader } from '@/components/ui/card'

export default function Approvals() {
  const plans = useLoad(() => api.plans('pending'), [], 5000)
  const needs = plans.data?.filter((p) => p.requires_approval && !p.approved_by) ?? []
  const other = plans.data?.filter((p) => !(p.requires_approval && !p.approved_by)) ?? []

  return (
    <>
      <PageHeader
        title="Approvals"
        description="Destructive changes to production apps wait here for a human. Approving does not apply the plan: the agent (or you) still applies it, with a reason."
      />
      {plans.error && <ErrorBox error={plans.error} />}

      <h2 className="mb-3 text-lg font-semibold">Waiting for approval</h2>
      {plans.loading && !plans.data && <CardsSkeleton count={1} height="h-40" />}
      {plans.data && needs.length === 0 && (
        <Empty title="Nothing waiting" icon={<CheckCircle2 />}>
          No destructive production changes are pending.
        </Empty>
      )}
      <div className="space-y-4">
        {needs.map((p) => (
          <PlanCard key={p.id} plan={p} onChange={plans.reload} />
        ))}
      </div>

      {other.length > 0 && (
        <>
          <h2 className="mt-8 mb-3 text-lg font-semibold">Other pending plans</h2>
          <div className="space-y-4">
            {other.map((p) => (
              <PlanCard key={p.id} plan={p} onChange={plans.reload} />
            ))}
          </div>
        </>
      )}
    </>
  )
}

function PlanCard({ plan, onChange }: { plan: Plan; onChange: () => void }) {
  const me = useSession()
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error>()
  const needsApproval = plan.requires_approval && !plan.approved_by

  async function approve() {
    setBusy(true)
    setError(undefined)
    try {
      await api.approve(plan.id)
      onChange()
    } catch (e) {
      setError(e as Error)
    } finally {
      setBusy(false)
      setConfirming(false)
    }
  }

  return (
    <Card
      role="article"
      className={cn(needsApproval && 'ring-2 ring-warn/60')}
      aria-label={`${plan.action} of ${plan.app}`}
    >
      <CardHeader className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2 font-semibold">
            <ToneBadge t={needsApproval ? 'warn' : 'muted'}>{plan.action}</ToneBadge>
            <Link className="hover:underline" to={`/apps/${plan.app}`}>
              {plan.app}
            </Link>
            {needsApproval && <span className="sr-only">(needs approval)</span>}
          </div>
          <Mono className="break-all text-xs text-muted-foreground">{plan.id}</Mono>
        </div>
        {needsApproval &&
          (me.scope !== 'admin' ? (
            <span className="text-xs text-muted-foreground">An admin token is needed to approve.</span>
          ) : !confirming ? (
            <Button variant="destructive" onClick={() => setConfirming(true)}>
              <ShieldCheck /> Approve…
            </Button>
          ) : (
            <div className="flex flex-wrap items-center gap-2">
              <Button variant="destructive" onClick={approve} disabled={busy} className="h-auto min-h-8 whitespace-normal py-1">
                {busy && <Loader2 className="animate-spin" aria-hidden />}
                {busy ? 'Approving…' : `Yes, approve ${plan.action} of ${plan.app}`}
              </Button>
              <Button variant="ghost" onClick={() => setConfirming(false)}>
                Cancel
              </Button>
            </div>
          ))}
      </CardHeader>
      <CardContent className="space-y-3">
        <PlanMeta plan={plan} />
        <ChangeList changes={plan.changes} />
        {error && <ErrorBox error={error} />}
      </CardContent>
      <CardFooter className="text-xs text-muted-foreground">Next: {plan.next_step}</CardFooter>
    </Card>
  )
}
