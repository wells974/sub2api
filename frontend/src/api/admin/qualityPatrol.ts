import { apiClient } from '../client'

export interface PatrolConfig {
  enabled: boolean
  quiet_enabled: boolean
  quiet_start: string
  quiet_end: string
  rules: string[]
}
export interface PatrolSettings {
  config: PatrolConfig
  timezone: string
  rules: { id: string; name: string }[]
  channels_configured: boolean
}

const path = '/admin/quality-patrol-notifications'
export async function getPatrolSettings() {
  return (await apiClient.get<PatrolSettings>(path)).data
}
export async function savePatrolSettings(config: PatrolConfig) {
  await apiClient.put(path, config)
}
