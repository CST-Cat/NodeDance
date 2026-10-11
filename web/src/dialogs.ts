import { shallowRef } from 'vue'

interface DialogRequest {
  kind: 'confirm' | 'prompt'
  message: string
  value: string
  resolve: (value: string | null) => void
}
export const activeDialog = shallowRef<DialogRequest | null>(null)
const queue: DialogRequest[] = []
function request(kind: DialogRequest['kind'], message: string, value = '') {
  return new Promise<string | null>(resolve => {
    const item = { kind, message, value, resolve }
    if (activeDialog.value) queue.push(item)
    else activeDialog.value = item
  })
}
export const requestPrompt = (message: string, value = '') => request('prompt', message, value)
export const requestConfirm = async (message: string) => (await request('confirm', message)) !== null
export function finishDialog(value: string | null) {
  const current = activeDialog.value
  activeDialog.value = queue.shift() ?? null
  current?.resolve(value)
}
export function cancelDialogs() {
  const pending = [activeDialog.value, ...queue]
  queue.length = 0
  activeDialog.value = null
  pending.forEach(item => item?.resolve(null))
}
