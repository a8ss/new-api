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

export interface CustomTokenDetails {
  id: number
  name: string
  key: string
  status: number
  expired_time: number
  remain_quota: number
  used_quota: number
  unlimited_quota: boolean
  custom_phone: string
  api_addresses: string[]
  models: string[]
}

export interface CustomShareLink {
  custom_share_code: string
  custom_share_url: string
}

export async function fetchCustomTokenShare(
  code: string,
  signal?: AbortSignal
): Promise<CustomTokenDetails> {
  const response = await api.post(
    '/api/custom/token-share',
    { custom_share_code: code },
    {
      signal,
      skipAuthRefresh: true,
      skipErrorHandler: true,
      withCredentials: false,
    }
  )
  return requireServerSuccess(response.data).data
}

export async function manageCustomTokenShare(
  id: number,
  action: 'view' | 'reset' | 'revoke'
): Promise<CustomShareLink> {
  const config = { singleUseAuthorization: true }
  let response
  if (action === 'revoke') {
    response = await api.delete(`/api/token/${id}/custom-share`, config)
  } else if (action === 'reset') {
    response = await api.put(`/api/token/${id}/custom-share`, {}, config)
  } else {
    response = await api.post(`/api/token/${id}/custom-share`, {}, config)
  }
  return requireServerSuccess(response.data).data
}
