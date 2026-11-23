import { apiClient } from '../client'
import type { OpenAIAPIKeyFallbackConfig, OpenAIAPIKeyFallbackStatus } from '@/utils/openaiAPIKeyFallback'

export interface GroupFallbackRule {
  group_id: number
  policy: OpenAIAPIKeyFallbackConfig
}
export interface GroupFallbackConfig {
  enabled: boolean
  revision: string
  rules: GroupFallbackRule[]
}
export interface GroupFallbackState {
  config: GroupFallbackConfig
  status: Record<number, OpenAIAPIKeyFallbackStatus>
}
export interface GroupFallbackOption {
  id: number
  name: string
  accounts: { id: number; name: string; schedulable: boolean }[]
}
const path = '/admin/group-fallback'
export async function getGroupFallback() {
  return (await apiClient.get<GroupFallbackState>(path)).data
}
export async function getGroupFallbackOptions() {
  return (await apiClient.get<GroupFallbackOption[]>(path + '/options')).data
}
export async function saveGroupFallback(config: GroupFallbackConfig) {
  return (await apiClient.put<GroupFallbackConfig>(path, config)).data
}
