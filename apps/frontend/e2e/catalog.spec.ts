import { catalogItem, expect, test } from '../../../tests/support/api-mock'

test('loads the real Go catalog through the frontend proxy', async ({ page }) => {
    await page.goto('/')

    await expect(page.getByRole('heading', { name: 'Последний трамвай' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Окно маяка' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Синий свет' })).toBeVisible()
    await expect(page.getByText('Видео недоступно')).toHaveCount(1)

    const posters = page.getByRole('listitem').locator('img')
    await expect(posters).toHaveCount(3)
    for (const poster of await posters.all()) {
        await poster.scrollIntoViewIfNeeded()
        await expect
            .poll(() =>
                poster.evaluate((image) =>
                    image instanceof HTMLImageElement ? image.naturalWidth : 0,
                ),
            )
            .toBeGreaterThan(0)
    }

    const icon = page.locator('link[rel="icon"][type="image/svg+xml"]')
    await expect(icon).toHaveAttribute('href', /\S/)
    const iconURL = await icon.evaluate((link: HTMLLinkElement) => link.href)
    expect(new URL(iconURL).origin).toBe(new URL(page.url()).origin)
    const response = await page.request.get(iconURL)
    expect(response.status()).toBe(200)
    expect(response.headers()['content-type']).toMatch(/^image\/svg\+xml(?:\s*;|$)/i)
})

test('keeps catalog items visible when a poster is unavailable', async ({ page, apiMock }) => {
    await apiMock.catalog.list({
        items: [
            catalogItem(),
            catalogItem({
                id: 'unknown',
                title: 'Запись без обложки',
                description: 'Описание записи',
                durationSeconds: 60,
            }),
        ],
    })
    await apiMock.catalog.missingPoster('/media/last-tram.webp')

    await Promise.all([
        page.waitForResponse(
            (response) =>
                response.url().endsWith('/media/last-tram.webp') && response.status() === 404,
        ),
        page.goto('/'),
    ])

    await expect(page.getByRole('heading', { name: 'Последний трамвай' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Запись без обложки' })).toBeVisible()
    await expect(page.getByRole('listitem').locator('img')).toHaveCount(0)
})

test('filters the real catalog by media type', async ({ page }) => {
    await page.goto('/')
    const choices = page.getByRole('group', { name: 'Тип медиа' })
    const audio = choices.getByRole('button', { name: 'Музыка' })
    await audio.click()

    await expect(audio).toHaveAttribute('aria-pressed', 'true')
    await expect(page.getByRole('heading', { name: 'Синий свет' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Последний трамвай' })).toHaveCount(0)
    await choices.getByRole('button', { name: 'Всё' }).click()
    await expect(page.getByRole('heading', { name: 'Последний трамвай' })).toBeVisible()
})

test('shows loading until the server returns an empty catalog', async ({ page, apiMock }) => {
    await apiMock.catalog.list(
        { items: [] },
        {
            beforeResponse: () => expect(page.getByRole('status')).toHaveText('Загружаем каталог…'),
        },
    )

    await page.goto('/')

    await expect(page.getByRole('status')).toHaveText('В каталоге пока нет фильмов.')
})

test('recovers from a server error when the viewer retries', async ({ page, apiMock }) => {
    await apiMock.catalog.failOnce(503)

    await page.goto('/')

    await expect(page.getByRole('alert')).toContainText('Не удалось загрузить каталог')
    await page.getByRole('button', { name: 'Повторить' }).click()
    await expect(page.getByRole('heading', { name: 'Последний трамвай' })).toBeVisible()
})

test('recovers from a catalog request that does not respond', async ({ page, apiMock }) => {
    await apiMock.catalog.timeOutOnce()

    await page.goto('/')

    await expect(page.getByRole('alert')).toContainText('Не удалось загрузить каталог', {
        timeout: 10_000,
    })
    await page.getByRole('button', { name: 'Повторить' }).click()
    await expect(page.getByRole('heading', { name: 'Последний трамвай' })).toBeVisible()
})

test('shows an error for an invalid catalog payload', async ({ page, apiMock }) => {
    await apiMock.catalog.invalidPayload()

    await page.goto('/')

    await expect(page.getByRole('alert')).toContainText('Не удалось загрузить каталог')
})

test('shows availability and omits a duration of zero', async ({ page, apiMock }) => {
    await apiMock.catalog.list({
        items: [
            catalogItem({
                id: 'ready',
                title: 'Доступный фильм',
                description: 'Описание',
                durationSeconds: 0,
                available: true,
            }),
        ],
    })

    await page.goto('/')

    const film = page.getByRole('listitem').filter({ hasText: 'Доступный фильм' })
    await expect(film.getByText('Доступен', { exact: true })).toBeVisible()
    await expect(film.getByText('0 с', { exact: true })).toHaveCount(0)
})
