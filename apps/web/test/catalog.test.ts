import { deepStrictEqual, strictEqual, throws } from 'node:assert/strict'
import { test } from 'node:test'
import type { CatalogItem } from '@kadrroom/contracts'
import { formatDuration, parseCatalog } from '../src/catalog.ts'

const item: CatalogItem = {
    id: 'test-film',
    title: 'Фильм',
    description: 'Описание',
    durationSeconds: 0,
    available: false,
}

test('parseCatalog accepts an empty catalog and valid records', () => {
    deepStrictEqual(parseCatalog({ items: [] }), [])
    deepStrictEqual(parseCatalog({ items: [item] }), [item])
})

test('parseCatalog rejects an invalid response envelope', () => {
    for (const response of [null, 1, {}, { items: null }, { items: {} }]) {
        throws(() => parseCatalog(response), /Invalid catalog/)
    }
})

test('parseCatalog rejects an invalid record anywhere in the catalog', () => {
    const invalidRecords = [
        null,
        {},
        { ...item, id: 1 },
        { ...item, title: null },
        { ...item, description: false },
        { ...item, durationSeconds: '1' },
        { ...item, durationSeconds: -1 },
        { ...item, durationSeconds: NaN },
        { ...item, durationSeconds: Infinity },
        { ...item, available: 'false' },
    ]

    for (const invalidRecord of invalidRecords) {
        throws(() => parseCatalog({ items: [item, invalidRecord] }), /Invalid catalog items/)
    }
})

test('formatDuration rounds and displays seconds, minutes and hours', () => {
    strictEqual(formatDuration(0), '0 с')
    strictEqual(formatDuration(58.6), '59 с')
    strictEqual(formatDuration(59.5), '1 мин')
    strictEqual(formatDuration(3600), '1 ч')
    strictEqual(formatDuration(3660), '1 ч 1 мин')
})
