/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { ApiKeysProvider } from '../components/api-keys-provider'
import { CustomShareDialog } from '../components/custom-share-dialog'
import type { CustomTokenDetails } from '../custom-share-api'
import { CustomSharePage } from '../custom-share-page'

let client: QueryClient
const code = 'AbCd1234'
const details: CustomTokenDetails = {
  id: 1,
  name: 'Mobile API key',
  key: 'full-secret-key',
  status: 1,
  expired_time: -1,
  remain_quota: 100,
  used_quota: 25,
  unlimited_quota: false,
  custom_phone: '****8000',
  api_addresses: ['https://api.example.com'],
  models: [`model-${'long-name-'.repeat(15)}`],
}

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
})
afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
})

function renderShare(shareCode = code) {
  return render(
    <QueryClientProvider client={client}>
      <CustomSharePage code={shareCode} />
    </QueryClientProvider>
  )
}

it('shows the masked phone and complete key on a narrow layout and copies the usable key', async () => {
  const user = userEvent.setup()
  const clipboard = vi.spyOn(navigator.clipboard, 'writeText')
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: details },
  })
  renderShare()
  expect(await screen.findByText(details.custom_phone)).toBeVisible()
  expect(screen.getByText('sk-full-secret-key')).toHaveClass('break-all')
  expect(screen.getByText(details.models[0])).toHaveClass('break-all')
  expect(screen.getByRole('main')).toHaveClass('w-full')
  await user.click(screen.getByRole('button', { name: 'Copy Key' }))
  expect(clipboard).toHaveBeenCalledWith('sk-full-secret-key')
  expect(api.post).toHaveBeenCalledWith(
    '/api/custom/token-share',
    { custom_share_code: code },
    expect.objectContaining({ withCredentials: false, skipAuthRefresh: true })
  )
})

it('hides stale credentials when refreshing a revoked share fails', async () => {
  const user = userEvent.setup()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({ data: { success: true, data: details } })
  renderShare()
  await screen.findByText('sk-full-secret-key')
  post.mockRejectedValueOnce(new Error('revoked'))
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(await screen.findByText('Share link is unavailable')).toBeVisible()
  expect(screen.queryByText('sk-full-secret-key')).not.toBeInTheDocument()
  expect(screen.queryByText(details.custom_phone)).not.toBeInTheDocument()
})

it('shows empty models and unlimited quota without inventing an account balance', async () => {
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: { ...details, models: [], custom_phone: '', unlimited_quota: true },
    },
  })
  renderShare()
  expect(await screen.findByText('No available models')).toBeVisible()
  expect(screen.getByText('Unlimited')).toBeVisible()
  expect(screen.queryByText('Phone number')).not.toBeInTheDocument()
})

it('does not query with a missing or malformed share code', () => {
  const post = vi.spyOn(api, 'post')
  renderShare('123456')
  expect(screen.getByText('Share link is unavailable')).toBeVisible()
  expect(post).not.toHaveBeenCalled()
})

it('shows loading without credentials until the share request completes', async () => {
  let complete!: (value: unknown) => void
  vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((resolve) => {
        complete = resolve
      })
  )
  renderShare()
  expect(screen.getByText('Loading...')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Copy Key' })
  ).not.toBeInTheDocument()
  await act(async () => complete({ data: { success: true, data: details } }))
  expect(await screen.findByRole('button', { name: 'Copy Key' })).toBeEnabled()
})

it('requires confirmation before rotating or disabling a share and displays the resulting link', async () => {
  const user = userEvent.setup()
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: { custom_share_url: '/ck/OldC1234' },
    },
  })
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: true,
      data: { custom_share_url: '/ck/NewC1234' },
    },
  })
  const remove = vi.spyOn(api, 'delete').mockResolvedValue({
    data: { success: true, data: { custom_share_url: '' } },
  })
  render(
    <QueryClientProvider client={client}>
      <ApiKeysProvider>
        <CustomShareDialog id={1} onClose={() => {}} />
      </ApiKeysProvider>
    </QueryClientProvider>
  )
  await screen.findByRole('button', { name: 'Copy share link' })
  await user.click(
    screen.getByRole('button', { name: 'Regenerate share link' })
  )
  expect(put).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  expect(await screen.findByText(/\/ck\/NewC1234/)).toBeVisible()
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  await user.click(screen.getByRole('button', { name: 'Disable sharing' }))
  expect(remove).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  expect(await screen.findByText('Sharing is disabled')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Disable sharing' })).toBeDisabled()
})
