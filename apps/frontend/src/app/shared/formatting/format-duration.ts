export function formatDuration(seconds: number): string {
    const total = Math.round(seconds)
    const hours = Math.floor(total / 3600)
    const minutes = Math.floor((total % 3600) / 60)

    if (hours > 0) {
        const hourLabel = `${String(hours)} ч`
        return minutes > 0 ? `${hourLabel} ${String(minutes)} мин` : hourLabel
    }
    if (minutes > 0) return `${String(minutes)} мин`

    return `${String(total)} с`
}
