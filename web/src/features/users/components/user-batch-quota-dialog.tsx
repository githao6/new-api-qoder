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
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import type { AdminUserBatchQuotaContext } from '@/features/auth/secure-verification'
import { getCurrencyDisplay, getCurrencyLabel } from '@/lib/currency'
import { formatQuota, parseQuotaFromDollars } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import {
  createServerError,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import { batchAdjustUserQuota, getBatchQuotaGroups } from '../api'
import type { BatchUserQuotaPayload, QuotaAdjustMode } from '../types'
import { useUsers } from './users-provider'

interface UserBatchQuotaDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

const QUOTA_MODES: readonly QuotaAdjustMode[] = ['add', 'subtract', 'override']

const MODE_LABELS: Record<QuotaAdjustMode, string> = {
  add: 'Add',
  subtract: 'Subtract',
  override: 'Override',
}

// One sentence per mode: interpolating a translated verb into a template
// reads badly in inflected languages.
const MODE_EFFECTS: Record<QuotaAdjustMode, string> = {
  add: 'Every targeted account gains {{amount}}',
  subtract: 'Every targeted account loses {{amount}}',
  override: 'Every targeted account is set to {{amount}}',
}

export function UserBatchQuotaDialog(props: UserBatchQuotaDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { triggerRefresh, requestVerification } = useUsers()
  const [scope, setScope] = useState<'groups' | 'all'>('groups')
  const [groups, setGroups] = useState<string[]>([])
  const [mode, setMode] = useState<QuotaAdjustMode>('add')
  const [amount, setAmount] = useState('')

  const { meta: currencyMeta } = getCurrencyDisplay()
  const currencyLabel = getCurrencyLabel()
  const tokensOnly = currencyMeta.kind === 'tokens'

  const { data: groupData } = useQuery({
    queryKey: ['quota-batch-groups'],
    queryFn: async () => requireServerSuccess(await getBatchQuotaGroups()),
    enabled: props.open,
    staleTime: 30 * 1000,
  })
  const listing = groupData?.data
  const quotaGroups = useMemo(() => listing?.groups ?? [], [listing])

  const options = useMemo(
    () =>
      quotaGroups.map((item) => ({
        label: item.group,
        value: item.group,
        hint: t('{{count}} users', { count: item.count }),
      })),
    [quotaGroups, t]
  )

  // The same totals the server uses to accept or skip a run, so the dialog
  // never offers a confirmation it cannot honour.
  const covered = useMemo(
    () =>
      scope === 'all'
        ? (listing?.total ?? 0)
        : quotaGroups
            .filter((item) => groups.includes(item.group))
            .reduce((sum, item) => sum + item.count, 0),
    [scope, groups, quotaGroups, listing]
  )

  const amountValue = Number.parseFloat(amount) || 0
  const quotaValue =
    mode === 'override'
      ? parseQuotaFromDollars(amountValue)
      : parseQuotaFromDollars(Math.abs(amountValue))

  const selectionLabel = (() => {
    if (scope === 'all') return t('All users')
    if (groups.length === 0) return t('No groups selected')
    return t('{{count}} groups', { count: groups.length })
  })()

  const run = useMutation({
    mutationFn: async () => {
      const all = scope === 'all'
      const value = quotaValue
      const context: AdminUserBatchQuotaContext = {
        groups: all ? [] : [...groups].sort(),
        all,
        mode,
        value,
      }
      const proof = await requestVerification({
        scope: 'admin.user.quota.batch',
        context,
        title: t('Verify to adjust user quota in bulk'),
        description: `${t(
          'Confirm your identity before you adjust the quota of {{count}} accounts.',
          { count: covered }
        )} ${effectSummary()}`,
      })
      if (!proof) return null
      const payload: BatchUserQuotaPayload = {
        groups: all ? [] : context.groups,
        all,
        mode,
        value,
      }
      const result = await batchAdjustUserQuota(payload, proof.proof_token)
      if (!result.success) throw createServerError(result)
      return result.data
    },
    onSuccess: (data) => {
      if (!data) return
      queryClient.invalidateQueries({ queryKey: ['quota-batch-groups'] })
      queryClient.invalidateQueries({ queryKey: ['users'] })
      if (data.affected === 0 && data.skipped === 0) {
        toast.info(
          t('{{count}} accounts already had this quota', {
            count: data.unchanged,
          })
        )
      } else if (data.skipped > 0) {
        toast.warning(
          t('Quota adjusted for {{affected}} of {{selection}} users', {
            affected: data.affected,
            selection: data.selection_size,
          }),
          {
            description: t('{{count}} accounts were skipped', {
              count: data.skipped,
            }),
          }
        )
      } else {
        toast.success(
          t('Quota adjusted for {{count}} users', { count: data.affected })
        )
      }
      props.onOpenChange(false)
      setGroups([])
      setAmount('')
      setMode('add')
      setScope('groups')
      triggerRefresh()
    },
    onError: (error) => {
      handleServerError(error, t('Failed to adjust user quota'))
    },
  })

  const amountMissing = mode === 'override' ? amount === '' : quotaValue <= 0
  const nothingSelected = scope === 'groups' && groups.length === 0
  const emptyPopulation = covered === 0

  const submitDisabled =
    run.isPending || amountMissing || nothingSelected || emptyPopulation

  const placeholder = tokensOnly
    ? t('Enter amount in tokens')
    : t('Enter amount in {{currency}}', { currency: currencyLabel })

  const effectSummary = () =>
    t(MODE_EFFECTS[mode], {
      amount: formatQuota(
        mode === 'override' ? quotaValue : Math.abs(quotaValue)
      ),
    })

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Batch Adjust Quota')}
      description={t(
        'Apply one quota operation to every account in the selected groups, or to all accounts at once.'
      )}
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            variant='outline'
            onClick={() => props.onOpenChange(false)}
            disabled={run.isPending}
          >
            {t('Cancel')}
          </Button>
          <Button onClick={() => run.mutate()} disabled={submitDisabled}>
            {run.isPending ? t('Processing...') : t('Confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <div className='space-y-2'>
          <div className='flex items-center justify-between gap-2'>
            <Label>{t('Target users')}</Label>
            <div className='flex items-center gap-2'>
              <Label className='text-muted-foreground text-sm font-normal'>
                {t('All users')}
              </Label>
              <Switch
                aria-label={t('All users')}
                checked={scope === 'all'}
                onCheckedChange={(checked) =>
                  setScope(checked ? 'all' : 'groups')
                }
              />
            </div>
          </div>
          {scope === 'all' ? (
            <p className='text-muted-foreground text-xs'>
              {t(
                'Every account is adjusted, including groups not listed here.'
              )}
            </p>
          ) : (
            <MultiSelect
              options={options}
              selected={groups}
              onChange={setGroups}
              maxVisibleChips={6}
              placeholder={t('Select groups')}
            />
          )}
        </div>

        <div className='space-y-2'>
          <Label>{t('Mode')}</Label>
          <div className='flex gap-1'>
            {QUOTA_MODES.map((m) => (
              <Button
                key={m}
                type='button'
                variant='outline'
                size='sm'
                className={cn(
                  mode === m &&
                    'bg-primary text-primary-foreground hover:bg-primary/90 hover:text-primary-foreground'
                )}
                onClick={() => {
                  setMode(m)
                  setAmount('')
                }}
              >
                {t(MODE_LABELS[m])}
              </Button>
            ))}
          </div>
        </div>

        <div className='space-y-2'>
          <Label htmlFor='batch-quota-amount'>
            {t('Amount')} ({currencyLabel})
          </Label>
          <Input
            id='batch-quota-amount'
            type='number'
            step={tokensOnly ? 1 : 0.000001}
            min={mode === 'override' ? undefined : 0}
            placeholder={placeholder}
            value={amount}
            onChange={(e) => setAmount(e.target.value)}
          />
        </div>

        <div className='text-muted-foreground space-y-1 text-sm'>
          <div>
            {t('Selection')}: {selectionLabel}
          </div>
          <div>{t('{{count}} accounts covered', { count: covered })}</div>
          {emptyPopulation && !nothingSelected ? (
            <div>{t('No active users are covered by this selection')}</div>
          ) : null}
          <div>{effectSummary()}</div>
          {mode === 'override' ? (
            <div>{t('The balance is set exactly and may become negative')}</div>
          ) : null}
          <div>
            {t(
              'Your own account and any account you may not manage are skipped automatically.'
            )}
          </div>
        </div>
      </div>
    </Dialog>
  )
}
