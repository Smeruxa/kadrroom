import type { CatalogItem, CatalogResponse } from '@kadrroom/contracts'

function isCatalogItem(item: unknown): item is CatalogItem {
    if (item === null || typeof item !== 'object') return false

    return (
        'id' in item &&
        typeof item.id === 'string' &&
        'title' in item &&
        typeof item.title === 'string' &&
        'description' in item &&
        typeof item.description === 'string' &&
        'durationSeconds' in item &&
        typeof item.durationSeconds === 'number' &&
        Number.isFinite(item.durationSeconds) &&
        item.durationSeconds >= 0 &&
        'available' in item &&
        typeof item.available === 'boolean'
    )
}

export function parseCatalog(data: unknown): CatalogResponse['items'] {
    if (data === null || typeof data !== 'object' || !('items' in data)) {
        throw new Error('Invalid catalog response')
    }
    if (!Array.isArray(data.items) || !data.items.every(isCatalogItem)) {
        throw new Error('Invalid catalog items')
    }

    return data.items
}

export function formatDuration(seconds: number): string {
    const total = Math.round(seconds)
    const hours = Math.floor(total / 3600)
    const minutes = Math.floor((total % 3600) / 60)

    if (hours > 0) return minutes > 0 ? `${hours} ч ${minutes} мин` : `${hours} ч`
    if (minutes > 0) return `${minutes} мин`

    return `${total} с`
}
