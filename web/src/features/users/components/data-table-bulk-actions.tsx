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
import { Power, PowerOff, Trash2 } from 'lucide-react'
import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { DataTableBulkActions as BulkActionsToolbar } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import type {
  AdminUserBatchDeleteContext,
  AdminUserBatchManageContext,
} from '@/features/auth/secure-verification'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { batchDeleteUsers, batchManageUsers } from '../api'
import { USER_ROLE, USER_STATUS } from '../constants'
import type { User } from '../types'
import { useUsers } from './users-provider'

type DataTableBulkActionsProps = {
  table: Table<User>
}

type BatchAction = 'disable' | 'enable' | 'delete'

// Mirrors the backend cap so an oversized selection fails before the step-up
// ceremony instead of burning a proof on a rejected request.
const MAX_BATCH_SIZE = 100

const BATCH_TITLES: Record<BatchAction, string> = {
  disable: 'Verify to disable selected users',
  enable: 'Verify to enable selected users',
  delete: 'Verify to delete selected users',
}

const BATCH_DESCRIPTIONS: Record<BatchAction, string> = {
  disable: 'Confirm your identity before disabling {{count}} accounts.',
  enable: 'Confirm your identity before enabling {{count}} accounts.',
  delete:
    'Confirm your identity before permanently deleting {{count}} accounts.',
}

const BATCH_SUCCESS: Record<BatchAction, string> = {
  disable: 'Successfully disabled {{count}} users',
  enable: 'Successfully enabled {{count}} users',
  delete: 'Successfully deleted {{count}} users',
}

const BATCH_FAILURE: Record<BatchAction, string> = {
  disable: 'Failed to disable {{count}} users',
  enable: 'Failed to enable {{count}} users',
  delete: 'Failed to delete {{count}} users',
}

const BATCH_LABELS: Record<BatchAction, string> = {
  disable: 'Disable selected users',
  enable: 'Enable selected users',
  delete: 'Delete selected users',
}

const BATCH_CONFIRM_TEXT: Record<BatchAction, string> = {
  disable: 'Disable',
  enable: 'Enable',
  delete: 'Delete',
}

export function DataTableBulkActions(props: DataTableBulkActionsProps) {
  const { t } = useTranslation()
  const { triggerRefresh, requestVerification, verificationActive } = useUsers()
  const currentUser = useAuthStore((state) => state.auth.user)
  const [pendingAction, setPendingAction] = useState<BatchAction | null>(null)
  const selectedRows = props.table.getFilteredSelectedRowModel().rows
  const myRole = currentUser?.role ?? 0
  const myId = currentUser?.id ?? 0

  // Mirror the server-side whole-batch rules so the buttons only ever offer a
  // selection the request can actually succeed with: no self-target, no
  // same-or-higher role, no root account, no soft-deleted rows.
  const manageable = useMemo(
    () =>
      selectedRows
        .map((row) => row.original)
        .filter(
          (user) =>
            user.id !== myId &&
            user.role < myRole &&
            user.role !== USER_ROLE.ROOT &&
            user.DeletedAt == null
        ),
    [selectedRows, myId, myRole]
  )

  const targets = useMemo(() => {
    const disable = manageable.filter(
      (user) => user.status !== USER_STATUS.DISABLED
    )
    const enable = manageable.filter(
      (user) => user.status === USER_STATUS.DISABLED
    )
    return { disable, enable, delete: manageable }
  }, [manageable])

  const batch = useMutation({
    mutationFn: async (action: BatchAction) => {
      const users = targets[action]
      const ids = users.map((user) => user.id)
      const title = t(BATCH_TITLES[action])
      const description = t(BATCH_DESCRIPTIONS[action], { count: ids.length })
      const request =
        action === 'delete'
          ? requestVerification({
              scope: 'admin.user.batch_delete',
              context: { user_ids: ids } satisfies AdminUserBatchDeleteContext,
              title,
              description,
            }).then((proof) =>
              proof ? batchDeleteUsers(ids, proof.proof_token) : null
            )
          : requestVerification({
              scope: 'admin.user.batch_manage',
              context: {
                user_ids: ids,
                action,
              } satisfies AdminUserBatchManageContext,
              title,
              description,
            }).then((proof) =>
              proof ? batchManageUsers(ids, action, proof.proof_token) : null
            )
      const result = await request
      if (!result) return null
      if (!result.success) throw createServerError(result)
      return { action, users, affected: result.data?.affected ?? 0 }
    },
    onSuccess: (outcome) => {
      if (!outcome) return
      // The users table keys selection by row index, so drop the whole
      // selection instead of un-checking the acted-on rows by id.
      props.table.resetRowSelection()
      toast.success(
        t(BATCH_SUCCESS[outcome.action], { count: outcome.affected })
      )
      setPendingAction(null)
      triggerRefresh()
    },
    onError: (error, action) => {
      const count = targets[action].length
      handleServerError(error, t(BATCH_FAILURE[action], { count }))
    },
  })

  const renderButton = (action: BatchAction, icon: ReactNode) => {
    const count = targets[action].length
    if (count === 0) return null
    const oversized = count > MAX_BATCH_SIZE
    const label = t(BATCH_LABELS[action])
    return (
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant={action === 'delete' ? 'destructive' : 'outline'}
              size='icon'
              className='size-8'
              aria-label={label}
              disabled={batch.isPending || oversized}
              onClick={() => setPendingAction(action)}
            />
          }
        >
          {icon}
        </TooltipTrigger>
        <TooltipContent>
          {oversized
            ? t(
                action === 'delete'
                  ? 'Maximum 100 users can be deleted in one batch'
                  : 'Maximum 100 users can be managed in one batch'
              )
            : label}
        </TooltipContent>
      </Tooltip>
    )
  }

  const pendingCount = pendingAction ? targets[pendingAction].length : 0

  return (
    <>
      <BulkActionsToolbar table={props.table} entityName={t('user')}>
        {renderButton('enable', <Power aria-hidden='true' />)}
        {renderButton('disable', <PowerOff aria-hidden='true' />)}
        {renderButton('delete', <Trash2 aria-hidden='true' />)}
      </BulkActionsToolbar>
      <ConfirmDialog
        destructive={pendingAction === 'delete'}
        open={pendingAction !== null && !verificationActive}
        onOpenChange={(open) => {
          if (!open && !batch.isPending) setPendingAction(null)
        }}
        title={
          pendingAction === 'delete'
            ? t('Delete {{count}} users?', { count: pendingCount })
            : t(
                pendingAction === 'enable'
                  ? 'Enable {{count}} users?'
                  : 'Disable {{count}} users?',
                { count: pendingCount }
              )
        }
        desc={t(
          pendingAction === 'delete'
            ? 'This will permanently delete the selected users. This action cannot be undone.'
            : 'The change applies to every selected account at once.'
        )}
        confirmText={
          pendingAction ? t(BATCH_CONFIRM_TEXT[pendingAction]) : t('Delete')
        }
        isLoading={batch.isPending}
        disabled={pendingCount === 0}
        handleConfirm={() => {
          if (pendingAction && !batch.isPending) batch.mutate(pendingAction)
        }}
      />
    </>
  )
}
