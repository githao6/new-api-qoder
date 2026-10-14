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
  getCoreRowModel,
  getFilteredRowModel,
  useReactTable,
} from '@tanstack/react-table'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useMemo, useState } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type { User } from '../../types'
import { DataTableBulkActions } from '../data-table-bulk-actions'
import { UsersProvider } from '../users-provider'

const originalAuth = useAuthStore.getState().auth

function user(id: number, overrides: Partial<User> = {}): User {
  return {
    id,
    username: `user-${id}`,
    display_name: `User ${id}`,
    role: 1,
    status: 1,
    quota: 0,
    used_quota: 0,
    request_count: 0,
    group: 'default',
    ...overrides,
  }
}

function mockVerification(scope: string, proofToken: string) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope,
            methods: [{ method: '2fa', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    return { data: { success: true, data: [] } }
  })
  return {
    data: {
      success: true,
      data: {
        proof_token: proofToken,
        method: '2fa',
        scope,
        expires_at: Math.floor(Date.now() / 1000) + 60,
      },
    },
  }
}

async function completeTwoFactorVerification() {
  await userEvent.type(
    await screen.findByLabelText('Authenticator code or backup code'),
    '123456'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Verify' }))
}

function Harness({
  rows,
  selectedIds,
}: {
  rows: User[]
  selectedIds: number[]
}) {
  const data = useMemo(() => rows, [rows])
  const [selection, setSelection] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(selectedIds.map((id) => [String(id), true]))
  )
  const table = useReactTable({
    data,
    columns: [{ id: 'id', accessorKey: 'id' }],
    state: { rowSelection: selection },
    onRowSelectionChange: setSelection,
    getCoreRowModel: getCoreRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getRowId: (row) => String(row.id),
    enableRowSelection: true,
  })
  return <DataTableBulkActions table={table} />
}

function renderBulk(rows: User[], selectedIds: number[]) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <UsersProvider>
        <Harness rows={rows} selectedIds={selectedIds} />
      </UsersProvider>
    </QueryClientProvider>
  )
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.setState({ auth: originalAuth })
})

function setOperator(id: number, role: number) {
  useAuthStore.setState({
    auth: { ...originalAuth, user: { ...user(id), role } },
  })
}

it('offers enable and disable only for the statuses present in the selection', () => {
  setOperator(1, 100)
  renderBulk([user(2), user(3, { status: 2 })], [2, 3])
  expect(
    screen.getByRole('button', { name: 'Disable selected users' })
  ).toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Enable selected users' })
  ).toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Delete selected users' })
  ).toBeInTheDocument()
})

it('hides every action when the selection holds no manageable account', () => {
  setOperator(1, 100)
  // Root, self, and same-or-higher-role peers are all excluded server-side.
  renderBulk(
    [
      user(1),
      user(9, { role: 100 }),
      user(10, { role: 100 }),
      user(11, { DeletedAt: '2026-01-01T00:00:00Z' }),
    ],
    [1, 9, 10, 11]
  )
  expect(
    screen.queryByRole('button', { name: 'Disable selected users' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Enable selected users' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Delete selected users' })
  ).not.toBeInTheDocument()
})

it('batch deletes with one proof bound to the manageable ID set', async () => {
  setOperator(1, 100)
  const proof = mockVerification(
    'admin.user.batch_delete',
    'batch-delete-proof'
  )
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/verify') return proof
    if (url === '/api/user/delete/batch') {
      return {
        data: {
          success: true,
          data: { affected_user_ids: [2], affected: 1, total: 1 },
        },
      }
    }
    throw new Error(`Unexpected POST ${url}`)
  })
  // The operator sees users 2 and 3; user 3 is an administrator, so a root
  // operator can still manage it, but user 4 is root and must be excluded.
  renderBulk([user(2), user(4, { role: 100 })], [2, 4])
  await userEvent.click(
    await screen.findByRole('button', { name: 'Delete selected users' })
  )
  const dialog = await screen.findByRole('alertdialog')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
  await completeTwoFactorVerification()
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/user/delete/batch',
      { ids: [2] },
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'batch-delete-proof' },
        singleUseAuthorization: true,
      })
    )
  )
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'admin.user.batch_delete',
      context: { user_ids: [2] },
    }),
    expect.anything()
  )
})

it('batch disables through the manage scope and refreshes the table', async () => {
  setOperator(1, 100)
  const proof = mockVerification(
    'admin.user.batch_manage',
    'batch-manage-proof'
  )
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/verify') return proof
    if (url === '/api/user/manage/batch') {
      return {
        data: {
          success: true,
          data: { affected_user_ids: [2], affected: 1, total: 1 },
        },
      }
    }
    throw new Error(`Unexpected POST ${url}`)
  })
  renderBulk([user(2), user(3)], [2, 3])
  await userEvent.click(
    await screen.findByRole('button', { name: 'Disable selected users' })
  )
  const dialog = await screen.findByRole('alertdialog')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Disable' }))
  await completeTwoFactorVerification()
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/user/manage/batch',
      { ids: [2, 3], action: 'disable' },
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'batch-manage-proof' },
        singleUseAuthorization: true,
      })
    )
  )
  expect(post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({
      scope: 'admin.user.batch_manage',
      context: { user_ids: [2, 3], action: 'disable' },
    }),
    expect.anything()
  )
})

it('sends nothing when the operator cancels the step-up ceremony', async () => {
  setOperator(1, 100)
  mockVerification('admin.user.batch_manage', 'unused')
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  renderBulk([user(2)], [2])
  await userEvent.click(
    await screen.findByRole('button', { name: 'Disable selected users' })
  )
  const dialog = await screen.findByRole('alertdialog')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Disable' }))
  await screen.findByLabelText('Authenticator code or backup code')
  expect(post).not.toHaveBeenCalledWith(
    '/api/user/manage/batch',
    expect.anything(),
    expect.anything()
  )
  await userEvent.keyboard('{Escape}')
  expect(
    post.mock.calls.filter(([url]) => url === '/api/user/manage/batch')
  ).toHaveLength(0)
})

it('disables the action when the selection exceeds the server batch cap', async () => {
  setOperator(1, 100)
  const rows = Array.from({ length: 101 }, (_, index) => user(index + 2))
  renderBulk(
    rows,
    rows.map((row) => row.id)
  )
  const button = await screen.findByRole('button', {
    name: 'Disable selected users',
  })
  expect(button).toBeDisabled()
  await userEvent.click(button)
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
})
