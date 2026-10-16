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
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export interface CustomSMSLog {
  id: number
  phone: string
  submit_status: string
  delivery_status: string
  error_code: string
  content: string
  actor_id: number
  created_at: number
  message_id?: string
  template_version?: string
  provider_status?: number
  provider_result?: number
  provider_stat?: string
}

export interface CustomSMSPage<T> {
  items: T[]
  total: number
  can_send?: boolean
  send_error?: string
  latest_unknown?: boolean
}

export interface CustomSMSToken {
  id: number
  name: string
  user_id: number
  source_token_id: number | null
  phone: string
}

export interface CustomSMSReply {
  id: number
  phone: string
  content: string
  time: string
}

export async function fetchCustomSMSHistory(
  id: number,
  page: number,
  admin = false,
  signal?: AbortSignal
): Promise<CustomSMSPage<CustomSMSLog>> {
  const path = admin
    ? `/api/sms/tokens/${id}/logs`
    : `/api/token/${id}/sms-logs`
  const response = await api.get(path, {
    params: { p: page, size: 10 },
    signal,
    skipErrorHandler: true,
  })
  return requireServerSuccess(response.data).data
}

export async function sendCustomSMS(
  id: number,
  key: string,
  admin = false
): Promise<void> {
  const path = admin
    ? `/api/sms/tokens/${id}/send`
    : `/api/token/${id}/sms-send`
  const response = await api.post(
    path,
    { idempotency_key: key },
    { singleUseAuthorization: true, skipErrorHandler: true }
  )
  requireServerSuccess(response.data)
}

export async function fetchCustomSMSTokens(
  page: number,
  signal?: AbortSignal
): Promise<CustomSMSPage<CustomSMSToken>> {
  const response = await api.get('/api/sms/tokens', {
    params: { p: page, size: 10 },
    signal,
    skipErrorHandler: true,
  })
  return requireServerSuccess(response.data).data
}

export async function fetchCustomSMSReplies(
  page: number,
  signal?: AbortSignal
): Promise<CustomSMSPage<CustomSMSReply>> {
  const response = await api.get('/api/sms/upstream', {
    params: { p: page, size: 10 },
    signal,
    skipErrorHandler: true,
  })
  return requireServerSuccess(response.data).data
}

export async function pullCustomSMSReplies(): Promise<void> {
  const response = await api.post(
    '/api/sms/upstream/pull',
    {},
    { singleUseAuthorization: true, skipErrorHandler: true }
  )
  requireServerSuccess(response.data)
}
