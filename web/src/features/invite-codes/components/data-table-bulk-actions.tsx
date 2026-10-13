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
import { useMutation } from '@tanstack/react-query'
import type { Table } from '@tanstack/react-table'
import { Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import { DataTableBulkActions as BulkActionsToolbar } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

import { batchDeleteInviteCodes } from '../api'
import { INVITE_CODE_STATUS } from '../constants'
import type { InviteCode } from '../types'
import { useInviteCodes } from './invite-codes-provider'

type DataTableBulkActionsProps = {
  table: Table<InviteCode>
}

export function DataTableBulkActions(props: DataTableBulkActionsProps) {
  const { t } = useTranslation()
  const { triggerRefresh } = useInviteCodes()
  const [deleteTargets, setDeleteTargets] = useState<InviteCode[] | null>(null)
  const selectedRows = props.table.getFilteredSelectedRowModel().rows

  const contentToCopy = useMemo(() => {
    const selectedCodes = selectedRows.map((row) => {
      const inviteCode = row.original
      return `${inviteCode.name}\t${inviteCode.key}`
    })
    return selectedCodes.join('\n')
  }, [selectedRows])

  const deletion = useMutation({
    mutationFn: async (targets: InviteCode[]) => {
      const result = await batchDeleteInviteCodes(
        targets.map((code) => code.id)
      )
      if (!result.success) throw createServerError(result)
      return result.data ?? 0
    },
    onSuccess: (_count, targets) => {
      toast.success(
        t('Successfully deleted {{count}} invite codes', { count: targets.length })
      )
      props.table.setRowSelection((previous) => {
        const next = { ...previous }
        for (const code of targets) delete next[String(code.id)]
        return next
      })
      setDeleteTargets(null)
      triggerRefresh()
    },
    onError: (_error, targets) => {
      handleServerError(
        _error,
        t('Failed to delete {{count}} invite codes', {
          count: targets.length,
        })
      )
    },
  })

  // Filter out used codes from selection for bulk delete
  const deletableSelectedRows = selectedRows.filter(
    (row) => row.original.status !== INVITE_CODE_STATUS.USED
  )

  return (
    <>
      <BulkActionsToolbar table={props.table} entityName={t('invite code')}>
        <CopyButton
          value={contentToCopy}
          variant='outline'
          size='icon'
          className='size-8'
          tooltip={t('Copy selected codes')}
          successTooltip={t('Codes copied!')}
          aria-label={t('Copy selected codes')}
        />
        {deletableSelectedRows.length > 0 && (
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant='destructive'
                  size='icon'
                  className='size-8'
                  aria-label={t('Delete selected invite codes')}
                  disabled={deletion.isPending}
                  onClick={() =>
                    setDeleteTargets(deletableSelectedRows.map((row) => row.original))
                  }
                />
              }
            >
              <Trash2 aria-hidden='true' />
            </TooltipTrigger>
            <TooltipContent>
              {t('Delete selected invite codes')}
            </TooltipContent>
          </Tooltip>
        )}
      </BulkActionsToolbar>
      <ConfirmDialog
        destructive
        open={deleteTargets !== null}
        onOpenChange={(open) => {
          if (!open && !deletion.isPending) setDeleteTargets(null)
        }}
        title={t('Delete {{count}} invite codes?', {
          count: deleteTargets?.length ?? 0,
        })}
        desc={t('This action cannot be undone.')}
        confirmText={deletion.isPending ? t('Deleting...') : t('Delete')}
        isLoading={deletion.isPending}
        disabled={!deleteTargets?.length}
        handleConfirm={() => {
          if (deleteTargets?.length && !deletion.isPending) {
            deletion.mutate(deleteTargets)
          }
        }}
      />
    </>
  )
}
