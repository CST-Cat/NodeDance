const beijingFormatter = new Intl.DateTimeFormat('sv-SE', {
  timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
})
export function beijingTime(value: string): string {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? beijingFormatter.format(date) : '—'
}
