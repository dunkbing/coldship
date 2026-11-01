import { createContext, useContext, useEffect, useState } from 'react'
import { NavLink, Navigate, Route, Routes } from 'react-router-dom'
import { api, ApiError, type WhoAmI } from './api'
import { useLoad } from './hooks'
import Login from './pages/Login'
import NewApp from './pages/NewApp'
import Data from './pages/Data'
import NewData from './pages/NewData'
import DataDetail from './pages/DataDetail'
import Apps from './pages/Apps'
import AppDetail from './pages/AppDetail'
import Approvals from './pages/Approvals'
import Audit from './pages/Audit'
import UpdateBanner from './components/UpdateBanner'

const SessionContext = createContext<WhoAmI | null>(null)
export const useSession = () => useContext(SessionContext)!

export default function App() {
  const [me, setMe] = useState<WhoAmI | null>(null)
  const [checking, setChecking] = useState(true)
  const [bootError, setBootError] = useState<Error>()

  useEffect(() => {
    api.whoami().then(
      (w) => {
        setMe(w)
        setChecking(false)
      },
      (e) => {
        if (!(e instanceof ApiError && e.status === 401)) setBootError(e)
        setChecking(false)
      },
    )
  }, [])

  // Any 401 later on (session expired, signed out elsewhere) returns to sign-in.
  useEffect(() => {
    const onSignedOut = () => setMe(null)
    window.addEventListener('coldship:signed-out', onSignedOut)
    return () => window.removeEventListener('coldship:signed-out', onSignedOut)
  }, [])

  if (checking) return <div className="boot">Loading…</div>
  if (!me) return <Login onLogin={setMe} initialError={bootError} />

  return (
    <SessionContext.Provider value={me}>
      <div className="shell">
        <Sidebar
          me={me}
          onSignOut={() => {
            api.logout().finally(() => setMe(null))
          }}
        />
        <main className="main">
          <UpdateBanner />
          <Routes>
            <Route path="/" element={<Navigate to="/apps" replace />} />
            <Route path="/apps" element={<Apps />} />
            <Route path="/apps/new" element={<NewApp />} />
            <Route path="/apps/:name" element={<AppDetail />} />
            <Route path="/data" element={<Data />} />
            <Route path="/data/new" element={<NewData />} />
            <Route path="/data/:name" element={<DataDetail />} />
            <Route path="/approvals" element={<Approvals />} />
            <Route path="/audit" element={<Audit />} />
            <Route path="*" element={<Navigate to="/apps" replace />} />
          </Routes>
        </main>
      </div>
    </SessionContext.Provider>
  )
}

function Sidebar({ me, onSignOut }: { me: WhoAmI; onSignOut: () => void }) {
  const pending = useLoad(() => api.plans('pending'), [], 10000)
  const needApproval = pending.data?.filter((p) => p.requires_approval && !p.approved_by).length ?? 0

  return (
    <nav className="sidebar" aria-label="Main">
      <div className="brand">
        <svg viewBox="0 0 32 32" width="26" height="26" aria-hidden>
          <rect width="32" height="32" rx="7" fill="var(--accent)" />
          <path d="M6 23V13l10-6 10 6v10h-4v-7H10v7z" fill="#fff" />
        </svg>
        <span>Coldship</span>
      </div>
      <div className="nav-links">
        <NavLink to="/apps">Apps</NavLink>
        <NavLink to="/data">Data &amp; Storage</NavLink>
        <NavLink to="/approvals">
          Approvals
          {needApproval > 0 && <span className="count">{needApproval}</span>}
        </NavLink>
        <NavLink to="/audit">Audit log</NavLink>
      </div>
      <div className="whoami">
        <div>
          <div className="whoami-name">{me.name}</div>
          <div className="muted small">scope: {me.scope}</div>
          {me.apps && me.apps.length > 0 && <div className="muted small">apps: {me.apps.join(', ')}</div>}
        </div>
        <button className="btn btn-ghost btn-sm" onClick={onSignOut}>
          Sign out
        </button>
      </div>
    </nav>
  )
}
