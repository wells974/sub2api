import { apiClient } from '../client'
import type { AccountOpsConfig } from './accountOps'

export interface AccountOpsPoolConfig {
  enabled: boolean
  notify_states: boolean
  notify_counts: boolean
}

export interface AccountOpsPoolDetails {
  available: number
  total: number
  previous_available?: number
  previous_total?: number
  changes: Array<{ account_id: number; account_name: string; before?: string; after: string }>
  omitted?: number
}

// A scoped update preserves channels and threshold rules, including concurrent
// changes from other administrators. There is no separate notification API.
export async function saveAccountOpsPool(config: AccountOpsPoolConfig): Promise<AccountOpsConfig> {
  return (await apiClient.put('/admin/account-ops/notification-settings', { pool_notifications: config })).data
}
