export function formatMiB(bytes: number): string {
  return (bytes / 1048576).toFixed(3) + ' MiB'
}

export function formatRequestBodySize(bytes: number): string {
  if (bytes === 0) return '0 B'
  if (bytes > 0 && bytes < 1024) return '<1 KiB'
  return formatMiB(bytes)
}
