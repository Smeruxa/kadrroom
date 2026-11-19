import { expect, test } from '@playwright/test'
import type { CatalogResponse } from '@kadrroom/contracts'

test('loads the real Go catalog through the frontend proxy', async ({ page }) => {
    await page.goto('/')

    await expect(page.getByRole('heading', { name: 'Последний трамвай' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Окно маяка' })).toBeVisible()
    await expect(page.getByText('Видео недоступно')).toHaveCount(2)
})

test('shows loading until the server returns an empty catalog', async ({ page }) => {
    const emptyCatalog: CatalogResponse = { items: [] }
    await page.route('**/v1/catalog', async (route) => {
        await expect(page.getByRole('status')).toHaveText('Загружаем каталог…')
        await route.fulfill({ json: emptyCatalog })
    })

    await page.goto('/')

    await expect(page.getByRole('status')).toHaveText('В каталоге пока нет фильмов.')
})

test('recovers from a server error when the viewer retries', async ({ page }) => {
    await page.route('**/v1/catalog', (route) => route.fulfill({ status: 503 }), { times: 1 })

    await page.goto('/')

    await expect(page.getByRole('alert')).toContainText('Не удалось загрузить каталог')
    await page.getByRole('button', { name: 'Повторить' }).click()
    await expect(page.getByRole('heading', { name: 'Последний трамвай' })).toBeVisible()
})

test('shows an error for an invalid catalog payload', async ({ page }) => {
    await page.route('**/v1/catalog', (route) => route.fulfill({ json: { items: null } }))

    await page.goto('/')

    await expect(page.getByRole('alert')).toContainText('Не удалось загрузить каталог')
})

test('shows availability and omits a duration of zero', async ({ page }) => {
    const catalog: CatalogResponse = {
        items: [
            {
                id: 'ready',
                title: 'Доступный фильм',
                description: 'Описание',
                durationSeconds: 0,
                available: true,
            },
        ],
    }
    await page.route('**/v1/catalog', (route) => route.fulfill({ json: catalog }))

    await page.goto('/')

    const film = page.getByRole('listitem').filter({ hasText: 'Доступный фильм' })
    await expect(film.getByText('Доступен', { exact: true })).toBeVisible()
    await expect(film.getByText('0 с', { exact: true })).toHaveCount(0)
})
