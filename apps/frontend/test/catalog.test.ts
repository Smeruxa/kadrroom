import { deepStrictEqual, strictEqual, throws } from 'node:assert/strict'
import { test } from 'node:test'
import type { CatalogItem } from '@kadrroom/contracts'
import { ZodError } from 'zod'
import { catalogSchema } from '../src/app/catalog/catalog.schema.ts'
import { formatDuration } from '../src/app/shared/formatting/format-duration.ts'
import { parsePayload } from '../src/app/shared/validation/parse-payload.ts'

const item: CatalogItem = {
    id: 'test-film',
    title: 'Фильм',
    description: 'Описание',
    durationSeconds: 0,
    available: false,
}

await test('the catalog schema accepts an empty catalog and valid records', () => {
    deepStrictEqual(parsePayload(catalogSchema, { items: [] }), { items: [] })
    deepStrictEqual(parsePayload(catalogSchema, { items: [item] }), { items: [item] })
    const covered = { ...item, coverUrl: '/v1/covers/cover-1' }
    deepStrictEqual(parsePayload(catalogSchema, { items: [covered] }), { items: [covered] })
})

await test('the catalog schema rejects an invalid response envelope', () => {
    for (const response of [null, 1, {}, { items: null }, { items: {} }]) {
        throws(() => parsePayload(catalogSchema, response), ZodError)
    }
})

await test('the catalog schema rejects an invalid record anywhere in the catalog', () => {
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
        { ...item, coverUrl: 'https://example.test/cover.png' },
    ]

    for (const invalidRecord of invalidRecords) {
        throws(() => parsePayload(catalogSchema, { items: [item, invalidRecord] }), ZodError)
    }
})

await test('formatDuration rounds and displays seconds, minutes and hours', () => {
    strictEqual(formatDuration(0), '0 с')
    strictEqual(formatDuration(58.6), '59 с')
    strictEqual(formatDuration(59.5), '1 мин')
    strictEqual(formatDuration(3600), '1 ч')
    strictEqual(formatDuration(3660), '1 ч 1 мин')
})
