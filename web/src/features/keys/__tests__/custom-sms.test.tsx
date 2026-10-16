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
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import {
  CustomSMSHistory,
  CustomSMSManagement,
} from '../components/custom-sms-dialog'

let client: QueryClient
beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
})
afterEach(async () => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
  await i18next.changeLanguage('en')
})

function showHistory(id = 7) {
  return render(
    <QueryClientProvider client={client}>
      <CustomSMSHistory tokenId={id} />
    </QueryClientProvider>
  )
}

it('allows an empty history to send only after confirmation and disables confirmation while pending', async () => {
  const user = userEvent.setup()
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { items: [], total: 0, can_send: true, send_error: '' },
    },
  })
  let finish: (() => void) | undefined
  const post = vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = () =>
          resolve({
            data: { success: true, data: { log_id: 1, status: 'queued' } },
          })
      })
  )
  showHistory()
  expect(await screen.findByText('No SMS records')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Send SMS' }))
  const confirmation = screen.getByRole('alertdialog', {
    name: 'Send this SMS?',
  })
  const confirm = within(confirmation).getByRole('button', { name: 'Send SMS' })
  await user.click(confirm)
  expect(confirm).toBeDisabled()
  expect(post).toHaveBeenCalledTimes(1)
  expect(post).toHaveBeenCalledWith(
    '/api/token/7/sms-send',
    { idempotency_key: expect.any(String) },
    expect.objectContaining({ singleUseAuthorization: true })
  )
  finish?.()
})

it('warns about duplicate delivery for an unknown result and cancelling never sends', async () => {
  const user = userEvent.setup()
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        items: [
          {
            id: 1,
            created_at: 1700000000,
            phone: '****8000',
            submit_status: 'accepted',
            delivery_status: 'unknown',
            error_code: 'report_timeout',
            content: '[share link]',
            actor_id: 1,
          },
        ],
        total: 1,
        can_send: true,
        send_error: '',
        latest_unknown: true,
      },
    },
  })
  const post = vi.spyOn(api, 'post')
  showHistory()
  await screen.findByText('****8000')
  await user.click(screen.getByRole('button', { name: 'Send SMS' }))
  expect(
    screen.getByText(
      'The previous result is unknown. The recipient may receive this SMS twice.'
    )
  ).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(post).not.toHaveBeenCalled()
})

it('keeps sending disabled when the latest attempt is still waiting for its delivery report', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        items: [],
        total: 0,
        can_send: false,
        send_error: 'send_in_progress',
      },
    },
  })
  showHistory()
  expect(
    await screen.findByText('SMS cannot be sent in the current state')
  ).toBeVisible()
  expect(screen.getByRole('button', { name: 'Send SMS' })).toBeDisabled()
})

it('keeps the idempotency key when a manual send fails and displays the rate limit', async () => {
  const user = userEvent.setup()
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [], total: 0, can_send: true } },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: false, message: 'rate_limited' } })
  showHistory()
  await screen.findByText('No SMS records')
  await user.click(screen.getByRole('button', { name: 'Send SMS' }))
  const dialog = screen.getByRole('alertdialog')
  await user.click(within(dialog).getByRole('button', { name: 'Send SMS' }))
  expect(await within(dialog).findByRole('alert')).toHaveTextContent(
    'Wait at least 60 seconds between manual sends; the daily limit is 5'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Send SMS' }))
  expect(post).toHaveBeenCalledTimes(2)
  expect(post.mock.calls[0]?.[1]).toEqual(post.mock.calls[1]?.[1])
})

it('only shows cross-user management to administrators with SMS read permission', () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'member', role: 1 })
  const view = render(
    <QueryClientProvider client={client}>
      <CustomSMSManagement />
    </QueryClientProvider>
  )
  expect(
    screen.queryByRole('button', { name: 'SMS management' })
  ).not.toBeInTheDocument()
  useAuthStore
    .getState()
    .auth.setUser({
      id: 2,
      username: 'admin',
      role: 10,
      permissions: {
        admin_permissions: { sms: { read: true, operate: false } },
      },
    })
  view.rerender(
    <QueryClientProvider client={client}>
      <CustomSMSManagement />
    </QueryClientProvider>
  )
  expect(screen.getByRole('button', { name: 'SMS management' })).toBeVisible()
})

it.each(['en', 'zh', 'zh-TW', 'fr', 'ja', 'ru', 'vi'])(
  'renders SMS actions and empty history in %s',
  async (language) => {
    const bundle = await import(`../../../i18n/locales/${language}.json`)
    i18next.addResourceBundle(
      language,
      'translation',
      bundle.translation,
      true,
      true
    )
    await i18next.changeLanguage(language)
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [], total: 0, can_send: true } },
    })
    showHistory()
    expect(
      await screen.findByText(bundle.translation['No SMS records'])
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: bundle.translation['Send SMS'] })
    ).toBeEnabled()
  }
)
