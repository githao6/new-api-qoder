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
import { z } from 'zod'

// ============================================================================
// Invite Code Schema & Types
// ============================================================================

export const inviteCodeSchema = z.object({
  id: z.number(),
  user_id: z.number(),
  name: z.string(),
  key: z.string(),
  status: z.number(), // 1: available, 2: disabled, 3: used
  created_time: z.number(),
  used_time: z.number(),
  expired_time: z.number(), // 0 for never expires
  used_user_id: z.number(),
  used_username: z.string(),
})

export type InviteCode = z.infer<typeof inviteCodeSchema>

// ============================================================================
// API Request/Response Types
// ============================================================================

export interface ApiResponse<T = unknown> {
  success: boolean
  message?: string
  data?: T
}

export interface GetInviteCodesParams {
  p?: number
  page_size?: number
}

export interface GetInviteCodesResponse {
  success: boolean
  message?: string
  data?: {
    items: InviteCode[]
    total: number
    page: number
    page_size: number
  }
}

export interface SearchInviteCodesParams {
  keyword?: string
  status?: string
  p?: number
  page_size?: number
}

export interface InviteCodeFormData {
  id?: number
  name: string
  count?: number // Only for create
  expired_time: number
  status?: number // Only for status update
}

// ============================================================================
// Dialog Types
// ============================================================================

export type InviteCodesDialogType = 'create' | 'update' | 'delete' | 'view'
