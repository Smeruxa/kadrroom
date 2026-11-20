import { accountSession, expect, test } from '../../../tests/support/api-mock'

test('opens the separate sign-in and restores the catalog panel for an operator', async ({
    page,
    apiMock,
}) => {
    const operator = accountSession()
    await apiMock.admin.session(operator, { signedIn: false })

    await page.goto('/')
    await expect(page).toHaveURL(/\/login$/)
    await page.getByLabel('Почта').fill(operator.email)
    await page.getByLabel('Пароль', { exact: true }).fill('test password for fixtures')
    await page.getByRole('button', { name: 'Войти', exact: true }).click()

    await expect(page.getByRole('heading', { name: 'Управление каталогом' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Новая запись' })).toBeVisible()
    await expect(page.getByText('Здесь появятся загруженные фильмы и аудиозаписи.')).toBeVisible()
    await page.getByRole('button', { name: 'Выйти' }).click()
    await expect(page.getByRole('heading', { name: 'Вход для операторов' })).toBeVisible()
})

test('tells a signed-in viewer when the catalog panel requires an operator', async ({
    page,
    apiMock,
}) => {
    await apiMock.admin.session(accountSession({ role: 'user' }))

    await page.goto('/')

    await expect(page.getByRole('alert')).toContainText('Управление каталогом доступно операторам.')
    await expect(page.getByRole('link', { name: 'Войти под другим аккаунтом' })).toHaveAttribute(
        'href',
        '/login',
    )
    await expect(page.getByRole('button', { name: 'Загрузить' })).toHaveCount(0)
})
