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
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import type { User } from '../../types'
import { UserBatchQuotaDialog } from '../user-batch-quota-dialog'
import { UsersProvider, useUsers } from '../users-provider'

// default covers 2 accounts, vip 3, empty has none; an all-users run reaches 6.
const GROUP_LISTING = {
  groups: [
    { group: 'default', count: 2 },
    { group: 'vip', count: 3 },
    { group: 'empty', count: 0 },
  ],
  total: 6,
}

const originalAuth = useAuthStore.getState().auth

const operator: User = {
  id: 1,
  username: 'operator',
  display_name: 'Operator',
  role: 100,
  status: 1,
  quota: 0,
  used_quota: 0,
  request_count: 0,
  group: 'default',
}

// The default currency configuration prices 1 USD at 500,000 quota units, so
// $2 typed into the dialog must reach the server as 2 * 500000 units.
const QUOTA_PER_USD = DEFAULT_CURRENCY_CONFIG.quotaPerUnit

function mockGroupListing() {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: 'admin.user.quota.batch',
            methods: [{ method: '2fa', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    if (url === '/api/user/quota/groups') {
      return { data: { success: true, data: GROUP_LISTING } }
    }
    return { data: { success: true, data: [] } }
  })
}

function issuedProof(proofToken: string) {
  return {
    data: {
      success: true,
      data: {
        proof_token: proofToken,
        method: '2fa',
        scope: 'admin.user.quota.batch',
        expires_at: Math.floor(Date.now() / 1000) + 60,
      },
    },
  }
}

function quotaData(overrides: Record<string, unknown>) {
  return {
    groups: ['vip'],
    all: false,
    mode: 'add',
    value: QUOTA_PER_USD,
    processed: 3,
    affected: 3,
    unchanged: 0,
    skipped: 0,
    skipped_by_reason: {
      self: 0,
      role: 0,
      deleted: 0,
      missing: 0,
      quota_limit: 0,
    },
    selection_size: 3,
    partial: false,
    ...overrides,
  }
}

function mockQuotaPost(proofToken: string, data: Record<string, unknown>) {
  const proof = issuedProof(proofToken)
  return vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/verify') return proof
    if (url === '/api/user/quota/batch') {
      return { data: { success: true, message: '', data } }
    }
    throw new Error(`Unexpected POST ${url}`)
  })
}

function Trigger() {
  const { setOpen } = useUsers()
  return (
    <button type='button' onClick={() => setOpen('batch_quota')}>
      Open batch quota
    </button>
  )
}

function DialogHost() {
  const { open, setOpen } = useUsers()
  return (
    <UserBatchQuotaDialog
      open={open === 'batch_quota'}
      onOpenChange={(isOpen) => !isOpen && setOpen(null)}
    />
  )
}

function renderDialog() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <UsersProvider>
        <Trigger />
        <DialogHost />
      </UsersProvider>
    </QueryClientProvider>
  )
}

async function openDialog() {
  await userEvent.click(
    await screen.findByRole('button', { name: 'Open batch quota' })
  )
  return screen.findByRole('dialog', { name: 'Batch Adjust Quota' })
}

async function selectGroup(dialog: HTMLElement, group: string) {
  await userEvent.click(within(dialog).getByRole('combobox'))
  await userEvent.click(await screen.findByRole('option', { name: group }))
}

async function typeAmount(dialog: HTMLElement, value: string) {
  await userEvent.type(within(dialog).getByLabelText('Amount (USD)'), value)
}

async function completeTwoFactorVerification() {
  await userEvent.type(
    await screen.findByLabelText('Authenticator code or backup code'),
    '123456'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Verify' }))
}

beforeEach(() => {
  useAuthStore.setState({
    auth: { ...originalAuth, user: { ...operator } },
  })
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.setState({ auth: originalAuth })
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})

it('converts the entered amount to quota units and binds one proof to the group selection', async () => {
  mockGroupListing()
  const post = mockQuotaPost('quota-batch-proof', quotaData({}))
  const success = vi.spyOn(toast, 'success')
  renderDialog()
  const dialog = await openDialog()

  const confirm = within(dialog).getByRole('button', { name: 'Confirm' })
  expect(confirm).toBeDisabled()

  await selectGroup(dialog, 'vip')
  await typeAmount(dialog, '2')
  expect(confirm).toBeEnabled()
  // The selection covers its own group, not the whole population.
  expect(dialog).toHaveTextContent('3 accounts covered')

  await userEvent.click(confirm)
  await completeTwoFactorVerification()

  const expected = {
    groups: ['vip'],
    all: false,
    mode: 'add',
    value: 2 * QUOTA_PER_USD,
  }
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/verify',
      expect.objectContaining({
        scope: 'admin.user.quota.batch',
        context: expected,
      }),
      expect.anything()
    )
  )
  expect(post).toHaveBeenCalledWith(
    '/api/user/quota/batch',
    expected,
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'quota-batch-proof' },
      singleUseAuthorization: true,
    })
  )
  await waitFor(() => expect(success).toHaveBeenCalled())
  expect(success).toHaveBeenCalledWith('Quota adjusted for 3 users')
})

it('treats an all-users override as an empty group list over the whole population', async () => {
  mockGroupListing()
  const post = mockQuotaPost(
    'quota-all-proof',
    quotaData({
      groups: null,
      all: true,
      mode: 'override',
      value: 0,
      processed: 6,
      affected: 5,
      unchanged: 1,
      selection_size: 6,
    })
  )
  renderDialog()
  const dialog = await openDialog()

  await userEvent.click(
    within(dialog).getByRole('switch', { name: 'All users' })
  )
  expect(dialog).toHaveTextContent('6 accounts covered')
  // The group picker is gone, so no narrower request can be issued.
  expect(within(dialog).queryByRole('combobox')).not.toBeInTheDocument()

  await userEvent.click(
    within(dialog).getByRole('button', { name: 'Override' })
  )
  // Override accepts zero, so an empty field is the only missing amount.
  await typeAmount(dialog, '0')
  expect(within(dialog).getByRole('button', { name: 'Confirm' })).toBeEnabled()
  await userEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }))
  await completeTwoFactorVerification()

  const expected = { groups: [], all: true, mode: 'override', value: 0 }
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/verify',
      expect.objectContaining({
        scope: 'admin.user.quota.batch',
        context: expected,
      }),
      expect.anything()
    )
  )
  expect(post).toHaveBeenCalledWith(
    '/api/user/quota/batch',
    expected,
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'quota-all-proof' },
      singleUseAuthorization: true,
    })
  )
})

it('refuses to confirm a selection that covers no accounts', async () => {
  mockGroupListing()
  const post = vi.spyOn(api, 'post')
  renderDialog()
  const dialog = await openDialog()

  await selectGroup(dialog, 'empty')
  await typeAmount(dialog, '1')
  expect(within(dialog).getByRole('button', { name: 'Confirm' })).toBeDisabled()
  expect(dialog).toHaveTextContent(
    'No active users are covered by this selection'
  )
  expect(post).not.toHaveBeenCalled()
})

it('surfaces the server skip summary instead of claiming a full success', async () => {
  mockGroupListing()
  mockQuotaPost(
    'quota-skip-proof',
    quotaData({ affected: 2, processed: 3, skipped: 1, selection_size: 3 })
  )
  const warning = vi.spyOn(toast, 'warning')
  const success = vi.spyOn(toast, 'success')
  renderDialog()
  const dialog = await openDialog()
  await selectGroup(dialog, 'vip')
  await typeAmount(dialog, '1')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }))
  await completeTwoFactorVerification()

  await waitFor(() => expect(warning).toHaveBeenCalled())
  expect(warning).toHaveBeenCalledWith('Quota adjusted for 2 of 3 users', {
    description: '1 accounts were skipped',
  })
  expect(success).not.toHaveBeenCalled()
})

it('sends nothing when the operator cancels the step-up ceremony', async () => {
  mockGroupListing()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  renderDialog()
  const dialog = await openDialog()
  await selectGroup(dialog, 'default')
  await typeAmount(dialog, '1')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }))
  await screen.findByLabelText('Authenticator code or backup code')
  await userEvent.keyboard('{Escape}')

  expect(
    post.mock.calls.filter(([url]) => url === '/api/user/quota/batch')
  ).toHaveLength(0)
})
