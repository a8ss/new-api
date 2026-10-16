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
import { useMutation, useQuery } from '@tanstack/react-query'
import { MessageSquare } from 'lucide-react'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  DataTablePagination,
  StaticDataTable,
  useDataTable,
} from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useIsAdmin } from '@/hooks/use-admin'
import { toIntlLocale } from '@/i18n/languages'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { formatNumber, formatTimestamp } from '@/lib/format'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import {
  fetchCustomSMSHistory,
  fetchCustomSMSReplies,
  fetchCustomSMSTokens,
  pullCustomSMSReplies,
  sendCustomSMS,
} from '../custom-sms-api'

const SMS_ERROR_KEYS: Record<string, string> = {
  storage_error: 'SMS storage is unavailable',
  source_missing: 'The source key must be linked before sending SMS',
  template_missing: 'No SMS template is configured for the source key',
  template_invalid:
    'The SMS template is empty or contains an unsupported variable',
  share_unavailable: 'The API key or its share link is unavailable',
  phone_invalid: 'The SMS phone number is invalid',
  configuration_invalid:
    'The SMS provider configuration is incomplete or invalid',
  provider_rejected: 'The SMS provider rejected this submission',
  response_unknown: 'The SMS submission result is unknown',
  process_interrupted: 'SMS processing was interrupted; delivery is unknown',
  report_timeout: 'No final delivery report was received within 24 hours',
  report_unknown: 'The delivery report contains an unknown status',
  delivery_failed: 'The SMS could not be delivered',
  rate_limited:
    'Wait at least 60 seconds between manual sends; the daily limit is 5',
  send_in_progress: 'SMS cannot be sent in the current state',
  callback_invalid: 'The SMS provider returned an invalid reply',
  request_invalid: 'The SMS request is invalid',
}
const SMS_STATUS_KEYS: Record<string, string> = {
  queued: 'Queued',
  sending: 'Sending',
  accepted: 'Submitted',
  failed: 'Failed',
  unknown: 'Unknown',
  pending: 'Waiting for delivery report',
  delivered: 'Delivered',
}

export function CustomSMSHistory(props: { tokenId: number; admin?: boolean }) {
  const { t, i18n } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const canOperate =
    !props.admin ||
    hasPermission(
      user,
      ADMIN_PERMISSION_RESOURCES.SMS,
      ADMIN_PERMISSION_ACTIONS.OPERATE
    )
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 10 })
  const [confirming, setConfirming] = useState(false)
  const requestKey = useRef<string | null>(null)
  const query = useQuery({
    queryKey: [
      'custom-sms-history',
      props.admin ?? false,
      props.tokenId,
      pagination.pageIndex,
    ],
    queryFn: ({ signal }) =>
      fetchCustomSMSHistory(
        props.tokenId,
        pagination.pageIndex + 1,
        props.admin,
        signal
      ),
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const mutation = useMutation({
    mutationFn: () => {
      requestKey.current ??= crypto.randomUUID()
      return sendCustomSMS(props.tokenId, requestKey.current, props.admin)
    },
    retry: false,
    meta: { errorToast: false },
    onSuccess: () => {
      requestKey.current = null
      setConfirming(false)
      void query.refetch()
    },
  })
  const items = query.data?.items ?? []
  const { table } = useDataTable({
    data: items,
    columns: [],
    manualPagination: true,
    totalCount: query.data?.total ?? 0,
    pagination,
    onPaginationChange: setPagination,
  })
  const unknown = query.data?.latest_unknown ?? false
  return (
    <div className='space-y-4'>
      <div className='flex flex-wrap items-center gap-2'>
        <Button
          variant='outline'
          onClick={() => void query.refetch()}
          disabled={query.isFetching}
        >
          {t('Refresh')}
        </Button>
        <Button
          onClick={() => setConfirming(true)}
          disabled={
            !query.data?.can_send ||
            !canOperate ||
            query.isFetching ||
            query.isError ||
            mutation.isPending
          }
        >
          {t('Send SMS')}
        </Button>
      </div>
      {query.isPending && <LoadingState message={t('Loading...')} />}
      {query.isError && <ErrorState onRetry={() => void query.refetch()} />}
      {query.data && !query.isError && (
        <>
          {query.data.send_error && (
            <p className='text-muted-foreground text-sm' role='status'>
              {t(
                SMS_ERROR_KEYS[query.data.send_error] ?? 'SMS could not be sent'
              )}
            </p>
          )}
          <StaticDataTable
            data={items}
            getRowKey={(row) => row.id}
            emptyContent={<EmptyState title={t('No SMS records')} />}
            columns={[
              {
                id: 'time',
                header: t('Time'),
                cell: (row) => formatTimestamp(row.created_at),
              },
              { id: 'phone', header: t('Phone'), cell: (row) => row.phone },
              {
                id: 'submission',
                header: t('Submission'),
                cell: (row) =>
                  t(SMS_STATUS_KEYS[row.submit_status] ?? 'Unknown'),
              },
              {
                id: 'delivery',
                header: t('Delivery'),
                cell: (row) =>
                  row.delivery_status
                    ? t(SMS_STATUS_KEYS[row.delivery_status] ?? 'Unknown')
                    : '—',
              },
              {
                id: 'actor',
                header: t('Operator'),
                cell: (row) => formatNumber(row.actor_id, locale),
              },
              {
                id: 'details',
                header: t('Details'),
                cellClassName: 'max-w-sm whitespace-normal break-words',
                cell: (row) => (
                  <div className='space-y-1'>
                    {row.error_code && (
                      <p>
                        {t(
                          SMS_ERROR_KEYS[row.error_code] ??
                            'SMS could not be sent'
                        )}
                      </p>
                    )}
                    <p>{row.content}</p>
                    {(row.provider_status != null ||
                      row.provider_result != null ||
                      row.provider_stat) && (
                      <p>
                        {t('Code')}:{' '}
                        {[
                          row.provider_status,
                          row.provider_result,
                          row.provider_stat,
                        ]
                          .filter(
                            (code) =>
                              code !== undefined && code !== null && code !== ''
                          )
                          .join(' / ')}
                      </p>
                    )}
                    {row.message_id && (
                      <p className='font-mono text-xs'>{row.message_id}</p>
                    )}
                  </div>
                ),
              },
            ]}
          />
          <DataTablePagination table={table} compact />
        </>
      )}
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={t('Send this SMS?')}
        desc={
          unknown
            ? t(
                'The previous result is unknown. The recipient may receive this SMS twice.'
              )
            : t('Send the current template to the original phone number?')
        }
        confirmText={t('Send SMS')}
        isLoading={mutation.isPending}
        disabled={!canOperate || !query.data?.can_send || query.isError}
        handleConfirm={() => mutation.mutate()}
      >
        {mutation.isError && (
          <p role='alert' className='text-destructive text-sm'>
            {t(
              SMS_ERROR_KEYS[getServerErrorMessage(mutation.error)] ??
                'SMS could not be sent'
            )}
          </p>
        )}
      </ConfirmDialog>
    </div>
  )
}

export function CustomSMSDialog(props: {
  tokenId: number
  onClose: () => void
}) {
  const { t } = useTranslation()
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && props.onClose()}
      title={t('SMS history')}
      contentClassName='sm:max-w-5xl'
    >
      <CustomSMSHistory tokenId={props.tokenId} />
    </Dialog>
  )
}

function CustomSMSKeyList(props: { onSelect: (id: number) => void }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 10 })
  const query = useQuery({
    queryKey: ['custom-sms-tokens', pagination.pageIndex],
    queryFn: ({ signal }) =>
      fetchCustomSMSTokens(pagination.pageIndex + 1, signal),
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const { table } = useDataTable({
    data: query.data?.items ?? [],
    columns: [],
    totalCount: query.data?.total ?? 0,
    manualPagination: true,
    pagination,
    onPaginationChange: setPagination,
  })
  if (query.isPending) return <LoadingState message={t('Loading...')} />
  if (query.isError) return <ErrorState onRetry={() => void query.refetch()} />
  return (
    <div className='space-y-4'>
      <StaticDataTable
        data={query.data.items}
        getRowKey={(row) => row.id}
        emptyContent={<EmptyState title={t('No Data')} />}
        columns={[
          {
            id: 'key',
            header: t('API Key'),
            cell: (row) => (
              <div>
                <span className='font-mono'>
                  {formatNumber(row.id, locale)}
                </span>
                <p className='max-w-sm break-words'>{row.name}</p>
              </div>
            ),
          },
          {
            id: 'owner',
            header: t('User ID'),
            cell: (row) => formatNumber(row.user_id, locale),
          },
          { id: 'phone', header: t('Phone'), cell: (row) => row.phone },
          {
            id: 'action',
            header: t('Actions'),
            cell: (row) => (
              <Button
                variant='outline'
                size='sm'
                onClick={() => props.onSelect(row.id)}
              >
                {t('SMS history')}
              </Button>
            ),
          },
        ]}
      />
      <DataTablePagination table={table} compact />
    </div>
  )
}

function CustomSMSInbox() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const canOperate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.SMS,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 10 })
  const query = useQuery({
    queryKey: ['custom-sms-replies', pagination.pageIndex],
    queryFn: ({ signal }) =>
      fetchCustomSMSReplies(pagination.pageIndex + 1, signal),
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const mutation = useMutation({
    mutationFn: pullCustomSMSReplies,
    retry: false,
    meta: { errorToast: false },
    onSuccess: () => {
      void query.refetch()
    },
  })
  const { table } = useDataTable({
    data: query.data?.items ?? [],
    columns: [],
    totalCount: query.data?.total ?? 0,
    manualPagination: true,
    pagination,
    onPaginationChange: setPagination,
  })
  return (
    <div className='space-y-4'>
      <Button
        variant='outline'
        disabled={!canOperate || mutation.isPending || query.isFetching}
        onClick={() => mutation.mutate()}
      >
        {t('Fetch incoming SMS')}
      </Button>
      {mutation.isError && (
        <p role='alert' className='text-destructive text-sm'>
          {t(
            SMS_ERROR_KEYS[getServerErrorMessage(mutation.error)] ??
              'SMS could not be sent'
          )}
        </p>
      )}
      {query.isPending && <LoadingState message={t('Loading...')} />}
      {query.isError && <ErrorState onRetry={() => void query.refetch()} />}
      {query.data && !query.isError && (
        <>
          <StaticDataTable
            data={query.data.items}
            getRowKey={(row) => row.id}
            emptyContent={<EmptyState title={t('No incoming SMS')} />}
            columns={[
              { id: 'time', header: t('Time'), cell: (row) => row.time },
              { id: 'phone', header: t('Phone'), cell: (row) => row.phone },
              {
                id: 'content',
                header: t('Content'),
                cellClassName: 'max-w-lg whitespace-normal break-words',
                cell: (row) => row.content,
              },
            ]}
          />
          <DataTablePagination table={table} compact />
        </>
      )}
    </div>
  )
}

export function CustomSMSManagement() {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const user = useAuthStore((state) => state.auth.user)
  const [open, setOpen] = useState(false)
  const [tokenId, setTokenId] = useState<number | null>(null)
  if (
    !isAdmin ||
    !hasPermission(
      user,
      ADMIN_PERMISSION_RESOURCES.SMS,
      ADMIN_PERMISSION_ACTIONS.READ
    )
  ) {
    return null
  }
  return (
    <>
      <Button
        variant='outline'
        size='sm'
        onClick={() => {
          setTokenId(null)
          setOpen(true)
        }}
      >
        <MessageSquare aria-hidden='true' />
        {t('SMS management')}
      </Button>
      <Dialog
        open={open}
        onOpenChange={setOpen}
        title={t('SMS management')}
        contentClassName='sm:max-w-5xl'
      >
        {open &&
          (tokenId === null ? (
            <Tabs defaultValue='keys'>
              <TabsList>
                <TabsTrigger value='keys'>{t('All SMS records')}</TabsTrigger>
                <TabsTrigger value='replies'>{t('Incoming SMS')}</TabsTrigger>
              </TabsList>
              <TabsContent value='keys'>
                <CustomSMSKeyList onSelect={setTokenId} />
              </TabsContent>
              <TabsContent value='replies'>
                <CustomSMSInbox />
              </TabsContent>
            </Tabs>
          ) : (
            <div className='space-y-4'>
              <Button variant='ghost' onClick={() => setTokenId(null)}>
                {t('Back')}
              </Button>
              <CustomSMSHistory key={tokenId} tokenId={tokenId} admin />
            </div>
          ))}
      </Dialog>
    </>
  )
}
