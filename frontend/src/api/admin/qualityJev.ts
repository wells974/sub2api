import { apiClient } from '../client'

export interface JevKey {
  id?: string
  masked?: string
  key?: string
  base_url: string
}

export interface JevSettings {
  revision: string
  enabled: boolean
  base_url: string
  proxy_id: number | null
  timeout_ms: number
  retry_count: number
  keys: JevKey[]
}

const path = '/admin/quality-jev-settings'

export async function getJevSettings() {
  return (await apiClient.get<JevSettings>(path)).data
}

export async function saveJevSettings(settings: JevSettings) {
  await apiClient.put(path, settings)
}
