import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import VikunjaSettings from './VikunjaSettings'
import { getVikunja, putVikunja, disconnectVikunja } from '../../lib/vikunja'
import { apiList } from '../../lib/apiClient'

vi.mock('../../lib/vikunja', () => ({
  getVikunja: vi.fn(),
  putVikunja: vi.fn(),
  disconnectVikunja: vi.fn(),
}))
vi.mock('../../lib/apiClient', () => ({
  apiList: vi.fn(),
  httpErrorMessage: (_: unknown, fallback: string) => fallback,
}))
vi.mock('../../hooks/useEventSources', () => ({ invalidateEventSources: vi.fn() }))
vi.mock('./stack/SettingsSection', () => ({
  default: ({
    children,
    onSave,
    saveDisabled,
  }: {
    children: React.ReactNode
    onSave: () => void
    saveDisabled: boolean
  }) => (
    <section>
      {children}
      <button disabled={saveDisabled} onClick={onSave}>
        Save connection
      </button>
    </section>
  ),
}))

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getVikunja).mockResolvedValue({
    base_url: 'https://tasks.example',
    projects: [{ project_id: 7, team_id: 'team' }],
    poll_interval_seconds: 60,
    connected: true,
  })
  vi.mocked(apiList).mockResolvedValue({
    items: [{ id: 'team', name: 'Engineering' }],
    total: 1,
  } as never)
  vi.mocked(putVikunja).mockResolvedValue(new Response(null, { status: 204 }))
})

describe('Vikunja settings', () => {
  it('keeps the installation fixed and sends only writable fields, preserving a blank token', async () => {
    render(<VikunjaSettings orgId="org" />)
    await waitFor(() =>
      expect(screen.getByLabelText('Installation URL')).toHaveValue('https://tasks.example'),
    )
    expect(screen.getByLabelText('Installation URL')).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Save connection' }))
    await waitFor(() =>
      expect(putVikunja).toHaveBeenCalledWith('org', {
        base_url: 'https://tasks.example',
        projects: [{ project_id: 7, team_id: 'team' }],
        poll_interval_seconds: 60,
        token: '',
      }),
    )
    expect(vi.mocked(putVikunja).mock.calls[0][1]).not.toHaveProperty('connected')
  })

  it('does not disconnect without confirmation', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    render(<VikunjaSettings orgId="org" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Disconnect Vikunja' }))
    expect(disconnectVikunja).not.toHaveBeenCalled()
    vi.restoreAllMocks()
  })
})
