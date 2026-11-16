import { apiFetch, apiJSON } from './apiClient'

export interface VikunjaRoute {
  project_id: number
  team_id: string
}

export interface VikunjaConnection {
  base_url: string
  projects: VikunjaRoute[]
  poll_interval_seconds: number
  connected: boolean
}

export function getVikunja(orgId: string) {
  return apiJSON<VikunjaConnection>(`/api/orgs/${encodeURIComponent(orgId)}/vikunja`)
}

export function putVikunja(
  orgId: string,
  connection: Omit<VikunjaConnection, 'connected'> & { token: string },
) {
  return apiFetch(`/api/orgs/${encodeURIComponent(orgId)}/vikunja`, {
    method: 'PUT',
    body: JSON.stringify(connection),
  })
}

export function disconnectVikunja(orgId: string) {
  return apiFetch(`/api/orgs/${encodeURIComponent(orgId)}/vikunja`, { method: 'DELETE' })
}
