import { useEffect, useState } from 'react'
import { apiList, httpErrorMessage } from '../../lib/apiClient'
import {
  getVikunja,
  putVikunja,
  disconnectVikunja,
  type VikunjaConnection,
} from '../../lib/vikunja'
import { LOCAL_DEFAULT_ORG_ID } from '../../lib/githubApp'
import { invalidateEventSources } from '../../hooks/useEventSources'
import type { TeamSummary } from '../../types'
import { glassInputClass } from './primitives'
import SettingsSection from './stack/SettingsSection'

const empty: VikunjaConnection = {
  base_url: '',
  projects: [],
  poll_interval_seconds: 60,
  connected: false,
}

export default function VikunjaSettings({ orgId }: { orgId: string | null }) {
  const org = orgId ?? LOCAL_DEFAULT_ORG_ID
  const [baseline, setBaseline] = useState(empty)
  const [draft, setDraft] = useState(empty)
  const [token, setToken] = useState('')
  const [teams, setTeams] = useState<TeamSummary[]>([])
  const [loaded, setLoaded] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoaded(false)
    setToken('')
    setError(null)
    Promise.all([
      getVikunja(org),
      apiList<TeamSummary>(`/api/orgs/${encodeURIComponent(org)}/teams/list`, { page_size: 200 }),
    ])
      .then(([connection, result]) => {
        if (cancelled) return
        setBaseline(connection)
        setDraft(connection)
        setTeams(result.items)
        setLoaded(true)
      })
      .catch((err) => {
        if (!cancelled) setError(httpErrorMessage(err, 'Could not load Vikunja settings.'))
      })
    return () => {
      cancelled = true
    }
  }, [org])

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      await putVikunja(org, {
        base_url: draft.base_url,
        projects: draft.projects,
        poll_interval_seconds: draft.poll_interval_seconds,
        token,
      })
      const connection = await getVikunja(org)
      setBaseline(connection)
      setDraft(connection)
      setToken('')
      invalidateEventSources(org)
      return true
    } catch (err) {
      setError(httpErrorMessage(err, 'Could not connect Vikunja.'))
      return false
    } finally {
      setBusy(false)
    }
  }

  return (
    <SettingsSection
      title="Vikunja"
      summary={baseline.connected ? baseline.base_url : 'Connect a task location'}
      dirty={token !== '' || JSON.stringify(draft) !== JSON.stringify(baseline)}
      saving={busy}
      saveDisabled={!loaded || !draft.base_url || draft.projects.length === 0}
      saveLabel={baseline.connected ? 'Save' : 'Connect'}
      onSave={save}
      onCancel={() => {
        setDraft(baseline)
        setToken('')
        setError(null)
      }}
    >
      <div className="space-y-4">
        {error && (
          <p role="alert" className="text-sm text-red-700">
            {error}
          </p>
        )}
        <p className="text-sm text-ink-3">
          Requires Vikunja 2.4.0 or later (API v2). Use an API token with permission to read
          projects and tasks, create and update tasks, and read and create task comments. Each
          project routes to one team; event handlers decide which events create tasks. Agents can
          create, edit, complete, reopen, and comment on tasks in their team’s routed projects.
          Writes are recorded in the audit log.
        </p>
        <label className="block text-sm text-ink-2">
          Installation URL
          <input
            className={glassInputClass}
            type="url"
            placeholder="https://tasks.example.com"
            value={draft.base_url}
            disabled={!loaded || busy || baseline.connected}
            onChange={(e) => setDraft({ ...draft, base_url: e.target.value })}
          />
        </label>
        <label className="block text-sm text-ink-2">
          API token {baseline.connected && '(leave blank to keep the current token)'}
          <input
            className={glassInputClass}
            type="password"
            autoComplete="new-password"
            value={token}
            disabled={!loaded || busy}
            onChange={(e) => setToken(e.target.value)}
          />
        </label>
        <label className="block text-sm text-ink-2">
          Poll interval (seconds)
          <input
            className={glassInputClass}
            type="number"
            min={30}
            max={86400}
            value={draft.poll_interval_seconds || 60}
            disabled={!loaded || busy}
            onChange={(e) => setDraft({ ...draft, poll_interval_seconds: Number(e.target.value) })}
          />
        </label>
        {draft.projects.map((route, i) => (
          <div key={i} className="flex flex-wrap items-end gap-2">
            <label className="min-w-32 flex-1 text-sm text-ink-2">
              Project ID
              <input
                className={glassInputClass}
                type="number"
                min={1}
                value={route.project_id || ''}
                disabled={busy}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    projects: draft.projects.map((p, n) =>
                      n === i ? { ...p, project_id: Number(e.target.value) } : p,
                    ),
                  })
                }
              />
            </label>
            <label className="min-w-40 flex-1 text-sm text-ink-2">
              Triage Factory team
              <select
                className={glassInputClass}
                value={route.team_id}
                disabled={busy}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    projects: draft.projects.map((p, n) =>
                      n === i ? { ...p, team_id: e.target.value } : p,
                    ),
                  })
                }
              >
                <option value="">Choose a team</option>
                {teams.map((team) => (
                  <option key={team.id} value={team.id}>
                    {team.name}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              disabled={busy}
              className="text-sm text-ink-3"
              onClick={() =>
                setDraft({ ...draft, projects: draft.projects.filter((_, n) => n !== i) })
              }
            >
              Remove
            </button>
          </div>
        ))}
        <button
          type="button"
          disabled={!loaded || busy}
          className="text-sm font-medium text-ink-2"
          onClick={() =>
            setDraft({
              ...draft,
              projects: [
                ...draft.projects,
                { project_id: 0, team_id: teams.length === 1 ? teams[0].id : '' },
              ],
            })
          }
        >
          Add project
        </button>
        {baseline.connected && (
          <button
            type="button"
            disabled={busy}
            className="block text-sm text-red-700"
            onClick={async () => {
              if (!window.confirm('Disconnect Vikunja? Existing tasks and history are kept.'))
                return
              setBusy(true)
              try {
                await disconnectVikunja(org)
                setBaseline(empty)
                setDraft(empty)
                setToken('')
                setError(null)
                invalidateEventSources(org)
              } catch (err) {
                setError(httpErrorMessage(err, 'Could not disconnect Vikunja.'))
              } finally {
                setBusy(false)
              }
            }}
          >
            Disconnect Vikunja
          </button>
        )}
      </div>
    </SettingsSection>
  )
}
