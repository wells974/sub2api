import { reactive, ref } from 'vue'
import { adminAPI } from '@/api/admin'

export interface NewAccountModelSync {
  id: number
  name: string
  state: 'syncing' | 'saved' | 'policy' | 'failed'
  count: number
}

export function useNewAccountModelSync(onRetrySaved: () => void) {
  const results = ref<NewAccountModelSync[]>([])

  async function run(row: NewAccountModelSync, signal?: AbortSignal) {
    row.state = 'syncing'
    try {
      const result = await adminAPI.accounts.syncUpstreamModels(row.id, { apply: true }, { timeout: 70000, signal })
      if (result.applied && !result.skipped) {
        row.count = result.models.length
        row.state = 'saved'
      } else {
        row.state = result.skipped === 'account_policy' ? 'policy' : 'failed'
      }
    } catch {
      row.state = 'failed'
    }
    return row.state === 'saved'
  }

  async function syncCreated(id: number, name: string, signal?: AbortSignal) {
    if (results.value.some(row => row.id === id)) return
    const row = reactive<NewAccountModelSync>({ id, name, state: 'syncing', count: 0 })
    results.value.push(row)
    await run(row, signal)
  }

  async function retry(row: NewAccountModelSync) {
    if (row.state === 'failed' && await run(row)) onRetrySaved()
  }

  return { results, syncCreated, retry }
}
