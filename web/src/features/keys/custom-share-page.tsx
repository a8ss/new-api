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
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatTimestamp } from '@/lib/format'

import { ApiKeyQuotaCell } from './components/api-key-quota-cell'
import { API_KEY_STATUSES } from './constants'
import { fetchCustomTokenShare } from './custom-share-api'

export function CustomSharePage(props: { code: string }) {
  const { t } = useTranslation()
  const validCode = /^[A-Za-z0-9]{8}$/.test(props.code)
  const query = useQuery({
    queryKey: ['custom-token-share', props.code],
    queryFn: ({ signal }) => fetchCustomTokenShare(props.code, signal),
    enabled: validCode,
    retry: false,
    gcTime: 0,
    staleTime: 0,
    refetchInterval: 30_000,
    meta: { errorToast: false },
  })
  const data = query.data
  const expired =
    data && data.expired_time !== -1 && data.expired_time * 1000 <= Date.now()
  const unavailable = !validCode || query.isError || expired
  let content
  if (unavailable) {
    content = (
      <ErrorState
        title={t('Share link is unavailable')}
        description={t('Ask the owner for a valid share link.')}
      />
    )
  } else if (!data) {
    content = <LoadingState message={t('Loading...')} />
  } else {
    const status = API_KEY_STATUSES[data.status]
    const addresses = data.api_addresses.length
      ? data.api_addresses
      : [window.location.origin]
    const key = data.key.startsWith('sk-') ? data.key : `sk-${data.key}`
    content = (
      <div className='space-y-4'>
        <Card>
          <CardHeader>
            <CardTitle className='break-words'>
              {data.name || t('API Key')}
            </CardTitle>
          </CardHeader>
          <CardContent className='space-y-5'>
            <div className='flex min-w-0 items-start gap-2'>
              <code className='min-w-0 flex-1 text-sm break-all select-text'>
                {key}
              </code>
              <CopyButton value={key} aria-label={t('Copy Key')} />
            </div>
            <ApiKeyQuotaCell apiKey={data} now={Date.now()} variant='card' />
            <dl className='grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-3 text-sm'>
              <dt className='text-muted-foreground'>{t('Status')}</dt>
              <dd className='text-right'>
                {status && (
                  <StatusBadge variant={status.variant}>
                    {t(status.label)}
                  </StatusBadge>
                )}
              </dd>
              <dt className='text-muted-foreground'>{t('Expiration Time')}</dt>
              <dd className='text-right break-words'>
                {data.expired_time === -1
                  ? t('Never expires')
                  : formatTimestamp(data.expired_time)}
              </dd>
              {data.custom_phone && (
                <>
                  <dt className='text-muted-foreground'>{t('Phone number')}</dt>
                  <dd className='text-right break-all select-text'>
                    {data.custom_phone}
                  </dd>
                </>
              )}
            </dl>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>{t('API Addresses')}</CardTitle>
          </CardHeader>
          <CardContent className='space-y-3'>
            {addresses.map((address) => (
              <div key={address} className='flex min-w-0 items-center gap-2'>
                <code className='min-w-0 flex-1 text-sm break-all select-text'>
                  {address}
                </code>
                <CopyButton
                  value={address}
                  aria-label={`${t('Copy API URL')}: ${address}`}
                />
              </div>
            ))}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>{t('Available Models')}</CardTitle>
          </CardHeader>
          <CardContent>
            {data.models.length ? (
              <ul className='grid gap-2 sm:grid-cols-2'>
                {data.models.map((name) => (
                  <li
                    key={name}
                    className='bg-muted rounded-md px-3 py-2 font-mono text-sm break-all'
                  >
                    {name}
                  </li>
                ))}
              </ul>
            ) : (
              <p className='text-muted-foreground text-sm'>
                {t('No available models')}
              </p>
            )}
          </CardContent>
        </Card>
        <Button
          variant='outline'
          className='w-full'
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          {t('Refresh')}
        </Button>
      </div>
    )
  }
  return (
    <main className='mx-auto min-h-svh w-full max-w-2xl px-4 py-6 sm:py-10'>
      <h1 className='mb-5 text-xl font-semibold'>{t('Share key')}</h1>
      {content}
    </main>
  )
}
