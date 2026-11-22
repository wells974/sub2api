export function formatMiB(bytes: number): string {
  return (bytes / 1048576).toFixed(3) + ' MiB'
}
