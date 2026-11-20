import { test as base } from '@playwright/test'
import type { Page, Route } from '@playwright/test'
import type { AccountSession, CatalogItem, CatalogResponse } from '@kadrroom/contracts'
import type { AdminItem } from '../../apps/admin-frontend/src/app/admin/admin-api'

export { expect } from '@playwright/test'

type RequestMethod = 'GET' | 'POST'
type Reply = (route: Route) => Promise<void> | void
type MockRequest = (
    method: RequestMethod,
    pathname: string,
    reply: Reply,
    options?: RequestOptions,
) => Promise<void>

interface RequestOptions {
    once?: boolean
}

interface CatalogListOptions {
    beforeResponse?: () => Promise<void>
}

interface CatalogMock {
    list(payload: CatalogResponse, options?: CatalogListOptions): Promise<void>
    failOnce(status: number): Promise<void>
    timeOutOnce(): Promise<void>
    invalidPayload(): Promise<void>
    missingPoster(pathname: string): Promise<void>
}

interface AdminSessionOptions {
    signedIn?: boolean
}

interface AdminCatalogResponse {
    items: AdminItem[]
}

interface AdminMock {
    session(account: AccountSession, options?: AdminSessionOptions): Promise<void>
}

interface ApiMock {
    catalog: CatalogMock
    admin: AdminMock
}

interface ApiMockFixtures {
    apiMock: ApiMock
}

const catalogPath = '/v1/catalog'

export const test = base.extend<ApiMockFixtures>({
    async apiMock({ page, baseURL }, use) {
        if (!baseURL) throw new Error('API mocks need a Playwright baseURL')

        const mockRequest = createRouter(page, new URL(baseURL).origin)
        await use({ catalog: catalog(mockRequest), admin: admin(mockRequest) })
    },
})

export function catalogItem(overrides: Partial<CatalogItem> = {}): CatalogItem {
    return {
        id: 'last-tram',
        title: 'Последний трамвай',
        description: 'Вечерний маршрут',
        durationSeconds: 240,
        available: false,
        ...overrides,
    }
}

export function accountSession(overrides: Partial<AccountSession> = {}): AccountSession {
    return {
        id: 'operator',
        email: 'operator@example.test',
        displayName: 'Оператор',
        role: 'operator',
        ...overrides,
    }
}

function createRouter(page: Page, origin: string): MockRequest {
    return async (method, pathname, reply, options) => {
        let consumed = false
        const match = (url: URL) => url.origin === origin && url.pathname === pathname
        const handle = async (route: Route): Promise<void> => {
            if (route.request().method() !== method || consumed) {
                await route.fallback()
                return
            }
            if (options?.once) consumed = true
            await reply(route)
        }
        await page.route(match, handle)
    }
}

function catalog(mockRequest: MockRequest): CatalogMock {
    return {
        list: (payload, options) =>
            mockRequest('GET', catalogPath, async (route) => {
                await options?.beforeResponse?.()
                await route.fulfill({ json: payload })
            }),
        failOnce: (status) =>
            mockRequest('GET', catalogPath, (route) => route.fulfill({ status }), { once: true }),
        timeOutOnce: () => mockRequest('GET', catalogPath, () => undefined, { once: true }),
        invalidPayload: () =>
            mockRequest('GET', catalogPath, (route) => route.fulfill({ json: { items: null } })),
        missingPoster: (pathname) =>
            mockRequest('GET', pathname, (route) => route.fulfill({ status: 404 })),
    }
}

function admin(mockRequest: MockRequest): AdminMock {
    return {
        async session(account, options) {
            let signedIn = options?.signedIn ?? true
            const catalog: AdminCatalogResponse = { items: [] }
            await mockRequest('GET', '/v1/auth/session', (route) =>
                route.fulfill(signedIn ? { json: account } : { status: 401 }),
            )
            await mockRequest('POST', '/v1/auth/login', (route) => {
                signedIn = true
                return route.fulfill({ json: account })
            })
            await mockRequest('POST', '/v1/auth/logout', (route) => {
                signedIn = false
                return route.fulfill({ status: 204 })
            })
            await mockRequest('GET', '/v1/admin/catalog', (route) => {
                if (!signedIn) return route.fulfill({ status: 401 })
                if (account.role !== 'operator') {
                    return route.fulfill({ status: 403, json: { code: 'forbidden' } })
                }
                return route.fulfill({ json: catalog })
            })
        },
    }
}
