import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type Plan } from '../api'
import { useSession } from '../App'
import { useLoad } from '../hooks'
import { ChangeList, Empty, ErrorBox, Mono, PlanMeta } from '@/components/common'

export default function Approvals() {
  const plans = useLoad(() => api.plans('pending'), [], 5000)
  const needs = plans.data?.filter((p) => p.requires_approval && !p.approved_by) ?? []
  const other = plans.data?.filter((p) => !(p.requires_approval && !p.approved_by)) ?? []

  return (
    <>
      <header className="page-head">
        <div>
          <h1>Approvals</h1>
          <p className="muted">
            Destructive changes to production apps wait here for a human. Approving does not apply the plan: the agent (or you) still
            applies it, with a reason.
          </p>
        </div>
      </header>
      {plans.error && <ErrorBox error={plans.error} />}

      <h2 className="section-title">Waiting for approval</h2>
      {plans.data && needs.length === 0 && <Empty title="Nothing waiting">No destructive production changes are pending.</Empty>}
      {needs.map((p) => (
        <PlanCard key={p.id} plan={p} onChange={plans.reload} />
      ))}

      {other.length > 0 && (
        <>
          <h2 className="section-title">Other pending plans</h2>
          {other.map((p) => (
            <PlanCard key={p.id} plan={p} onChange={plans.reload} />
          ))}
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
    <article className={`card plan-card${needsApproval ? ' needs-approval' : ''}`}>
      <div className="plan-head">
        <div>
          <div className="plan-title">
            <span className="badge">{plan.action}</span> <Link to={`/apps/${plan.app}`}>{plan.app}</Link>
          </div>
          <Mono>{plan.id}</Mono>
        </div>
        {needsApproval &&
          (me.scope !== 'admin' ? (
            <span className="muted small">An admin token is needed to approve.</span>
          ) : !confirming ? (
            <button className="btn btn-danger" onClick={() => setConfirming(true)}>
              Approve…
            </button>
          ) : (
            <div className="inline">
              <button className="btn btn-danger" onClick={approve} disabled={busy}>
                {busy ? 'Approving…' : `Yes, approve ${plan.action} of ${plan.app}`}
              </button>
              <button className="btn btn-ghost" onClick={() => setConfirming(false)}>
                Cancel
              </button>
            </div>
          ))}
      </div>
      <PlanMeta plan={plan} />
      <ChangeList changes={plan.changes} />
      {error && <ErrorBox error={error} />}
      <div className="small muted">Next: {plan.next_step}</div>
    </article>
  )
}
