import { randomUUID } from 'node:crypto'
import { expect, test } from '@playwright/test'
import type { Locator, Page } from '@playwright/test'
import { existingMessageIds, waitForAccountLink } from './mailpit'
import type { AccountPurpose } from './mailpit'

const webAddress = 'KADRROOM_E2E_BASE_URL'
const adminAddress = 'KADRROOM_E2E_ADMIN_URL'
const mailAddress = 'KADRROOM_E2E_MAIL_URL'
const rtcFlag = 'KADRROOM_E2E_RTC'
const requiredInputs = {
    web: [webAddress, mailAddress],
    admin: [webAddress, mailAddress, adminAddress],
    rtc: [webAddress, mailAddress, rtcFlag],
}
const loadStack = {
    web: webStack,
    admin: adminStack,
    rtc: rtcStack,
}

interface WebStack {
    webOrigin: string
    mailOrigin: string
}

interface AdminStack extends WebStack {
    adminOrigin: string
    operatorEmail: string
}

export interface Credentials {
    email: string
    password: string
}

interface NewAccount {
    emailPrefix: string
}

interface OperatorAccount {
    operatorEmail: string
}

type Flow = keyof typeof loadStack
type PrivateLink = AccountPurpose | 'join'
type MailRequest = '/register' | '/forgot-password'

export function useRealStack<Scenario extends Flow>(flow: Scenario): (typeof loadStack)[Scenario] {
    const inputs = requiredInputs[flow]
    const enabled = inputs.every((key) =>
        key === rtcFlag ? process.env[key] === '1' : Boolean(process.env[key]),
    )
    test.use({ trace: 'off', screenshot: 'off', video: 'off' })
    test.skip(!enabled, `Required testing inputs: ${inputs.join(', ')}`)
    return loadStack[flow]
}

export async function prepareAccount(
    page: Page,
    stack: WebStack,
    name: string,
    account: NewAccount | OperatorAccount,
): Promise<Credentials> {
    const allowReset = 'operatorEmail' in account
    const email = allowReset
        ? account.operatorEmail
        : `${account.emailPrefix}-${randomUUID()}@example.test`
    const password = `KadrRoom-${randomUUID()}`
    const mailbox = { origin: stack.mailOrigin, email }
    const previous = allowReset ? await existingMessageIds(mailbox) : new Set<string>()

    await requestAccountMail(page, stack.webOrigin, email, '/register')
    if (allowReset) await requestAccountMail(page, stack.webOrigin, email, '/forgot-password')
    const { link, purpose } = await waitForAccountLink(mailbox, previous, allowReset)
    await openPrivateLink(page, link, stack.webOrigin, purpose)

    const main = page.getByRole('main')
    if (purpose === 'verify') {
        await expect(main.getByRole('heading', { name: 'Завершить регистрацию' })).toBeVisible()
        await main.getByLabel('Имя в комнате', { exact: true }).fill(name)
        await fillPassword(main.getByLabel('Пароль', { exact: true }), password)
        await main.getByRole('button', { name: 'Создать аккаунт', exact: true }).click()
        await expect(main.getByRole('status')).toHaveText('Аккаунт подтверждён.')
    } else {
        await fillPassword(main.getByLabel('Новый пароль', { exact: true }), password)
        await main.getByRole('button', { name: 'Сохранить пароль', exact: true }).click()
        await expect(main.getByRole('status')).toHaveText('Пароль изменён.')
    }
    return { email, password }
}

export async function signInToWeb(
    page: Page,
    stack: WebStack,
    account: Credentials,
): Promise<void> {
    const main = page.getByRole('main')
    const login = main.getByRole('link', { name: 'Войти', exact: true })
    if (await login.isVisible()) {
        await login.click()
    } else {
        await page.goto(new URL('/login', stack.webOrigin).href)
    }
    await expect(main.getByRole('heading', { name: 'Вход в KadrRoom' })).toBeVisible()
    await main.getByLabel('Почта', { exact: true }).fill(account.email)
    await fillPassword(main.getByLabel('Пароль', { exact: true }), account.password)
    await main.getByRole('button', { name: 'Войти', exact: true }).click()
    await expect(page.getByRole('link', { name: 'Аккаунт', exact: true })).toBeVisible()
}

export async function fillPassword(field: Locator, password: string): Promise<void> {
    await expect(field).toBeEditable()
    await field.evaluate((input: HTMLInputElement, value) => {
        input.value = value
        input.dispatchEvent(new Event('input', { bubbles: true }))
    }, password)
}

export async function openPrivateLink(
    page: Page,
    value: string,
    origin: string,
    kind: PrivateLink,
): Promise<void> {
    const pathname = `/${kind}`
    if (!URL.canParse(value)) throw new Error(`Invalid ${pathname} link`)

    const url = new URL(value)
    if (
        url.origin !== origin ||
        url.pathname !== pathname ||
        url.username ||
        url.password ||
        !url.searchParams.get(kind === 'join' ? 'invite' : 'token')
    ) {
        throw new Error(`Invalid ${pathname} link for the configured frontend`)
    }
    await page.evaluate((destination) => {
        requestAnimationFrame(() => {
            location.assign(destination)
        })
    }, url.href)
    await page.waitForFunction(
        (path) => {
            if (location.pathname !== path || !document.querySelector('main form')) return false

            history.replaceState(history.state, '', path)
            return true
        },
        pathname,
        { timeout: 15_000 },
    )
}

function webStack(): WebStack {
    return { webOrigin: originFromEnv(webAddress), mailOrigin: originFromEnv(mailAddress) }
}

function adminStack(): AdminStack {
    const operatorEmail = (process.env['KADRROOM_E2E_OPERATOR_EMAIL'] ?? 'operator@example.test')
        .trim()
        .toLowerCase()
    if (!operatorEmail) throw new Error('KADRROOM_E2E_OPERATOR_EMAIL is empty')
    return { ...webStack(), adminOrigin: originFromEnv(adminAddress), operatorEmail }
}

function rtcStack(): WebStack {
    const stack = webStack()
    checkLocalOrigin(stack.webOrigin, webAddress, '4174')
    checkLocalOrigin(stack.mailOrigin, mailAddress, '8026')
    return stack
}

function originFromEnv(variable: string): string {
    const value = process.env[variable] ?? ''
    if (!URL.canParse(value)) throw new Error(`${variable} must be an absolute HTTP(S) URL`)

    const url = new URL(value)
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) {
        throw new Error(`${variable} must be an HTTP(S) URL without credentials`)
    }
    return url.origin
}

function checkLocalOrigin(origin: string, variable: string, productionPort: string): void {
    const url = new URL(origin)
    if (
        url.protocol !== 'http:' ||
        !['127.0.0.1', 'localhost'].includes(url.hostname) ||
        !url.port ||
        url.port === productionPort
    ) {
        throw new Error(`${variable} must refer to the local testing environment`)
    }
}

async function requestAccountMail(
    page: Page,
    webOrigin: string,
    email: string,
    path: MailRequest,
): Promise<void> {
    const address = new URL(path, webOrigin).href
    if (page.url() !== address) await page.goto(address)

    const main = page.getByRole('main')
    const heading = path === '/register' ? 'Создать аккаунт' : 'Восстановить пароль'
    await expect(main.getByRole('heading', { name: heading, exact: true })).toBeVisible()
    await main.getByLabel('Почта', { exact: true }).fill(email)
    await main.getByRole('button', { name: 'Отправить ссылку', exact: true }).click()
    await expect(main.getByRole('status')).toContainText('Проверьте почту.')
}
