import { randomUUID } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { fillPassword, prepareAccount, useRealStack } from '../support/real-stack'
import type { Credentials } from '../support/real-stack'

const getStack = useRealStack('admin')
const demoAudio = fileURLToPath(
    new URL('../../apps/frontend/public/demo-media/blue-light.m4a', import.meta.url),
)

test.use({ actionTimeout: 10_000 })
test.skip(
    ({ browserName }) => browserName !== 'chromium',
    'The operator is shared by the test stack',
)

test('only an operator can publish and unpublish a processed demo recording', async ({
    page: operator,
    browser,
}) => {
    test.setTimeout(150_000)
    const stack = getStack()
    const { adminOrigin, webOrigin, operatorEmail } = stack
    const title = `Демо для проверки публикации ${randomUUID()}`

    await test.step('Deny catalog management to anonymous visitors and regular viewers', async () => {
        const viewer = await browser.newPage()
        try {
            await viewer.goto(adminOrigin)
            await expect(viewer.getByRole('heading', { name: 'Вход для операторов' })).toBeVisible()
            await expect(
                viewer.getByRole('button', { name: 'Загрузить', exact: true }),
            ).toHaveCount(0)

            const viewerCredentials = await prepareAccount(viewer, stack, 'Зритель проверки', {
                emailPrefix: 'admin-viewer',
            })
            await signInToAdmin(viewer, adminOrigin, viewerCredentials)
            await expect(viewer.getByRole('alert')).toContainText(
                'Управление каталогом доступно операторам.',
            )
            await expect(
                viewer.getByRole('button', { name: 'Загрузить', exact: true }),
            ).toHaveCount(0)
        } finally {
            await viewer.close()
        }
    })

    await test.step('Prepare the configured test operator through email and sign in', async () => {
        const operatorCredentials = await prepareAccount(operator, stack, 'Оператор проверки', {
            operatorEmail,
        })
        await signInToAdmin(operator, adminOrigin, operatorCredentials)
        await expect(operator.getByRole('heading', { name: 'Новая запись' })).toBeVisible()
    })

    const row = operator.getByRole('listitem').filter({
        has: operator.getByRole('heading', { name: title, exact: true }),
    })
    const ready = row.getByText('Готово к публикации', { exact: true })
    const publish = row.getByRole('button', { name: 'Опубликовать', exact: true })
    const unpublish = row.getByRole('button', { name: 'Снять с публикации', exact: true })

    await test.step('Upload demo audio and wait for the media worker to finish', async () => {
        const upload = operator.getByRole('region', { name: 'Новая запись' })
        await upload.getByLabel('Название', { exact: true }).fill(title)
        await upload
            .getByLabel('Описание', { exact: true })
            .fill('Проверка обработки и публикации.')
        await upload.getByLabel('Тип медиа', { exact: true }).selectOption('audio')
        await upload.getByLabel('Медиафайл', { exact: true }).setInputFiles(demoAudio)
        await upload.getByRole('button', { name: 'Загрузить', exact: true }).click()
        await expect(ready).toBeVisible({ timeout: 60_000 })
        await expect(publish).toBeEnabled()
    })

    const publicPage = await browser.newPage()
    try {
        const listing = publicPage.getByRole('heading', { name: title, exact: true })
        await openCatalog(publicPage, webOrigin)
        await expect(listing).toHaveCount(0)

        await test.step('Publish the recording and check it in the public catalog', async () => {
            await publish.click()
            try {
                await expect(row.getByText('Опубликовано', { exact: true })).toBeVisible()
                await openCatalog(publicPage, webOrigin)
                const card = publicPage.getByRole('listitem').filter({ has: listing })
                await expect(card.getByText('Доступен', { exact: true })).toBeVisible()
            } finally {
                if (await unpublish.isVisible()) await unpublish.click()
            }
        })

        await test.step('Confirm the unpublished recording is absent from the public catalog', async () => {
            await expect(ready).toBeVisible()
            await openCatalog(publicPage, webOrigin)
            await expect(listing).toHaveCount(0)
        })
    } finally {
        await publicPage.close()
    }
})

async function signInToAdmin(page: Page, adminOrigin: string, account: Credentials): Promise<void> {
    await page.goto(new URL('/login', adminOrigin).href)
    await page.getByLabel('Почта', { exact: true }).fill(account.email)
    await fillPassword(page.getByLabel('Пароль', { exact: true }), account.password)
    await page.getByRole('button', { name: 'Войти', exact: true }).click()
}

async function openCatalog(page: Page, webOrigin: string): Promise<void> {
    await page.goto(webOrigin)
    await expect(
        page.getByRole('heading', { name: 'Последний трамвай', exact: true }),
    ).toBeVisible()
}
