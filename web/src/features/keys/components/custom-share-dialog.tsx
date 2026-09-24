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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'

import { manageCustomTokenShare } from '../custom-share-api'
import { useApiKeys } from './api-keys-provider'

export function CustomShareDialog(props: { id: number; onClose: () => void }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const { triggerRefresh } = useApiKeys()
  const [action, setAction] = useState<'reset' | 'revoke' | null>(null)
  const queryKey = ['custom-share-link', props.id]
  const query = useQuery({
    queryKey,
    queryFn: () => manageCustomTokenShare(props.id, 'view'),
    retry: false,
    gcTime: 0,
  })
  const mutation = useMutation({
    mutationFn: (operation: 'reset' | 'revoke') =>
      manageCustomTokenShare(props.id, operation),
    onSuccess: (data) => {
      client.setQueryData(queryKey, data)
      setAction(null)
      triggerRefresh()
    },
  })
  const link = query.data?.custom_share_url
  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => !open && props.onClose()}
        title={t('Share key')}
        description={t(
          'Anyone with this link can view the full API key and phone number.'
        )}
      >
        {query.isPending && <LoadingState message={t('Loading...')} />}
        {query.isError && <ErrorState onRetry={() => void query.refetch()} />}
        {query.data && (
          <div className='space-y-4'>
            {link ? (
              <div className='flex min-w-0 items-start gap-2'>
                <code className='min-w-0 flex-1 text-xs break-all'>
                  {new URL(link, window.location.origin).href}
                </code>
                <CopyButton
                  value={new URL(link, window.location.origin).href}
                  aria-label={t('Copy share link')}
                />
              </div>
            ) : (
              <p className='text-muted-foreground text-sm'>
                {t('Sharing is disabled')}
              </p>
            )}
            <div className='flex flex-wrap gap-2'>
              <Button
                variant='outline'
                disabled={mutation.isPending}
                onClick={() => setAction('reset')}
              >
                {t('Regenerate share link')}
              </Button>
              <Button
                variant='destructive'
                disabled={!link || mutation.isPending}
                onClick={() => setAction('revoke')}
              >
                {t('Disable sharing')}
              </Button>
            </div>
          </div>
        )}
      </Dialog>
      <ConfirmDialog
        open={action !== null}
        onOpenChange={(open) => !open && setAction(null)}
        title={
          action === 'revoke'
            ? t('Disable sharing')
            : t('Regenerate share link')
        }
        desc={t(
          'The previous link will stop working. API keys already copied will remain usable.'
        )}
        isLoading={mutation.isPending}
        handleConfirm={() => action && mutation.mutate(action)}
      />
    </>
  )
}
