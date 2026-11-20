import { randomUUID } from 'node:crypto'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import { existingMessageIds, waitForAccountLink } from '../support/mailpit'
import {
    fillPassword,
    openPrivateLink,
    prepareAccount,
    signInToWeb,
    useRealStack,
} from '../support/real-stack'

const getStack = useRealStack('web')

test('password recovery consumes the email link and revokes existing sessions', async ({
    page: first,
    browser,
}) => {
    test.setTimeout(90_000)
    const stack = getStack()
    const loginURL = new URL('/login', stack.webOrigin).href
    const profileURL = new URL('/account', stack.webOrigin).href
    const sessionURL = new URL('/v1/auth/session', stack.webOrigin).href
    const second = await browser.newPage()
    const recovery = await browser.newPage()

    try {
        const oldAccount =
            await test.step('Sign in to the same account in two browser contexts', async () => {
                const account = await prepareAccount(
                    first,
                    stack,
                    'Участник проверки восстановления',
                    {
                        emailPrefix: 'account-recovery',
                    },
                )
                await Promise.all([
                    signInToWeb(first, stack, account),
                    signInToWeb(second, stack, account),
                ])
                await expectSessions([first, second], sessionURL, 200)
                return account
            })

        const resetLink =
            await test.step('Request a reset from a signed-out browser and receive the email', async () => {
                const mailbox = { origin: stack.mailOrigin, email: oldAccount.email }
                const previous = await existingMessageIds(mailbox)
                const main = recovery.getByRole('main')
                await recovery.goto(loginURL)
                await main.getByRole('link', { name: 'Забыли пароль?', exact: true }).click()
                await expect(
                    main.getByRole('heading', { name: 'Восстановить пароль', exact: true }),
                ).toBeVisible()
                const email = main.getByLabel('Почта', { exact: true })
                await email.fill(oldAccount.email)
                const [response] = await Promise.all([
                    recovery.waitForResponse(
                        (result) =>
                            result.url() === `${stack.webOrigin}/v1/auth/password/reset-request` &&
                            result.request().method() === 'POST',
                        { timeout: 10_000 },
                    ),
                    email.press('Enter'),
                ])
                expect(response.status()).toBe(202)
                await expect(main.getByRole('status')).toContainText('Проверьте почту.')

                const { link, purpose } = await waitForAccountLink(mailbox, previous, true)
                expect(purpose).toBe('reset')
                return link
            })

        const newAccount = { ...oldAccount, password: `KadrRoom-${randomUUID()}` }
        await test.step('Set a new password and check both previous sessions are revoked', async () => {
            await openPrivateLink(recovery, resetLink, stack.webOrigin, 'reset')
            await submitNewPassword(recovery, newAccount.password)
            await expect(recovery.getByRole('main').getByRole('status')).toHaveText(
                'Пароль изменён.',
            )

            await expectSessions([first, second], sessionURL, 401)
            await second.goto(profileURL)
            await expect(
                second.getByRole('main').getByRole('link', { name: 'Войдите в аккаунт' }),
            ).toBeVisible()
        })

        await test.step('Reject the used link and keep the new password active', async () => {
            await recovery.goto(loginURL)
            await openPrivateLink(recovery, resetLink, stack.webOrigin, 'reset')
            await submitNewPassword(recovery, `KadrRoom-${randomUUID()}`)
            const main = recovery.getByRole('main')
            await expect(main.getByRole('alert')).toHaveText('Ссылка истекла или уже использована.')
            await expect(main.getByRole('link', { name: 'Запросить новую ссылку' })).toBeVisible()

            await first.goto(loginURL)
            const login = first.getByRole('main')
            await login.getByLabel('Почта', { exact: true }).fill(oldAccount.email)
            await fillPassword(login.getByLabel('Пароль', { exact: true }), oldAccount.password)
            await login.getByRole('button', { name: 'Войти', exact: true }).click()
            await expect(login.getByRole('alert')).toHaveText('Проверьте почту и пароль.')
            await expectSessions([first], sessionURL, 401)

            await signInToWeb(first, stack, newAccount)
            await expectSessions([first], sessionURL, 200)
        })
    } finally {
        await Promise.all([second.close(), recovery.close()])
    }
})

async function submitNewPassword(page: Page, password: string): Promise<void> {
    const main = page.getByRole('main')
    await fillPassword(main.getByLabel('Новый пароль', { exact: true }), password)
    await main.getByRole('button', { name: 'Сохранить пароль', exact: true }).click()
}

async function expectSessions(
    pages: Page[],
    sessionURL: string,
    expectedStatus: number,
): Promise<void> {
    const statuses = await Promise.all(
        pages.map(async (page) => {
            const response = await page.request.get(sessionURL, { timeout: 5_000 })
            return response.status()
        }),
    )
    expect(statuses).toEqual(pages.map(() => expectedStatus))
}
