import { randomUUID } from 'node:crypto'
import { expect, test } from '@playwright/test'
import type { Locator, Page, WebSocketRoute } from '@playwright/test'
import type { RoomCommand } from '@kadrroom/contracts'
import { z } from 'zod'
import { openPrivateLink, prepareAccount, signInToWeb, useRealStack } from '../support/real-stack'

const title = 'Последний трамвай'
const hostName = 'Ведущий тестовой комнаты'
const guestName = 'Гость тестовой комнаты'
const timeline = 'Позиция воспроизведения'
const positionTolerance = 0.35

const socketMessageSchema = z.discriminatedUnion('type', [
    z.object({ type: z.literal('snapshot'), state: z.object({ revision: z.number().int() }) }),
    z.object({ type: z.literal('error'), code: z.string() }),
])

const getStack = useRealStack('web')

test('verified host and browser guest share a real room timeline', async ({
    page: host,
    browser,
}) => {
    test.setTimeout(120_000)
    const stack = getStack()
    const { webOrigin } = stack
    const hostMain = host.getByRole('main')

    await test.step('Register, verify the address and sign in', async () => {
        await host.goto(webOrigin)
        await host.getByRole('link', { name: 'Войти', exact: true }).click()
        await hostMain.getByRole('link', { name: 'Создать аккаунт', exact: true }).click()
        const account = await prepareAccount(host, stack, hostName, { emailPrefix: 'web-flow' })
        await signInToWeb(host, stack, account)
    })

    const invitation =
        await test.step('Create a room from the catalog and read the invitation', async () => {
            const film = hostMain
                .getByRole('listitem')
                .filter({ has: host.getByRole('heading', { name: title, exact: true }) })
            await film.getByRole('button', { name: 'Создать комнату', exact: true }).click()
            const link = hostMain.getByLabel('Ссылка для гостя', { exact: true })
            const value = await link.evaluate(
                (field: HTMLInputElement) => {
                    const invitation = field.value
                    field.value = ''
                    return invitation
                },
                undefined,
                { timeout: 15_000 },
            )
            await expect(
                hostMain.getByRole('heading', { name: title, exact: true, level: 1 }),
            ).toBeVisible()
            await expect(syncStatus(host)).toBeVisible()
            return value
        })

    const guestContext = await browser.newContext({ viewport: { width: 375, height: 812 } })
    try {
        const guest = await guestContext.newPage()
        const guestMain = guest.getByRole('main')
        let guestSocket: WebSocketRoute | undefined
        let guestRevision: number | undefined
        let rejectedCommands = 0
        let disconnected = false

        await guest.routeWebSocket(
            (url) => /^\/v1\/rooms\/[^/]+\/ws$/.test(url.pathname),
            async (route) => {
                if (disconnected) {
                    await route.close({ code: 1000 })
                    return
                }

                const server = route.connectToServer()
                guestSocket = server
                server.onMessage((frame) => {
                    route.send(frame)
                    if (typeof frame !== 'string') return

                    const message = readSocketMessage(frame)
                    if (message?.type === 'snapshot') guestRevision = message.state.revision
                    if (message?.type === 'error' && message.code === 'forbidden')
                        rejectedCommands++
                })
            },
        )

        await test.step('Join as a guest and check the narrow layout and guest permissions', async () => {
            await guest.goto(webOrigin)
            await expect(guest.getByRole('link', { name: 'Войти', exact: true })).toBeVisible()
            await openPrivateLink(guest, invitation, webOrigin, 'join')
            await expect(guestMain.getByRole('heading', { name: 'Войти в комнату' })).toBeVisible()
            await expectNoHorizontalOverflow(guest, 'Guest invitation')
            await guestMain.getByLabel('Имя в комнате', { exact: true }).fill(guestName)
            await guestMain.getByRole('button', { name: 'Присоединиться', exact: true }).click()
            await expect(
                guestMain.getByRole('heading', { name: title, exact: true, level: 1 }),
            ).toBeVisible()
            await expect(syncStatus(guest)).toBeVisible()
            await expect(guestMain.getByText('Гость · Вы', { exact: true })).toBeVisible()
            await expect(guestMain.getByText(hostName, { exact: true })).toBeVisible()
            await expect(hostMain.getByText(guestName, { exact: true })).toBeVisible()
            await expect(guestMain.getByRole('progressbar', { name: timeline })).toBeVisible()
            await expect(guestMain.getByRole('slider', { name: 'Громкость' })).toBeEnabled()
            await expect(guestMain.getByRole('slider', { name: timeline })).toHaveCount(0)
            await expect(
                guestMain.getByRole('button', { name: /^(Воспроизвести|Пауза)$/ }),
            ).toHaveCount(0)
            await expectNoHorizontalOverflow(guest, 'Guest room')

            if (!guestSocket || guestRevision === undefined) {
                throw new Error('The authenticated guest WebSocket did not receive a snapshot')
            }

            const guestActions: RoomCommand['action'][] = ['play', 'pause', 'seek']
            for (const [index, action] of guestActions.entries()) {
                const command: RoomCommand = {
                    type: 'command',
                    action,
                    commandId: randomUUID(),
                    expectedRevision: guestRevision,
                    ...(action === 'seek' ? { positionMs: 1000 } : {}),
                }
                guestSocket.send(JSON.stringify(command))
                await expect
                    .poll(() => rejectedCommands, { message: `The server rejects guest ${action}` })
                    .toBe(index + 1)
            }
        })

        const hostVideo = roomVideo(host)
        const guestVideo = roomVideo(guest)
        const hostSeek = hostMain.getByRole('slider', { name: timeline })
        const play = hostMain.getByRole('button', { name: 'Воспроизвести', exact: true })
        const pause = hostMain.getByRole('button', { name: 'Пауза', exact: true })
        await expect(hostSeek).toBeEnabled({ timeout: 15_000 })
        const duration = await hostVideo.evaluate((video: HTMLVideoElement) => video.duration)

        const firstTarget =
            await test.step('Play, pause and seek on both real media elements', async () => {
                await expect(guestVideo).toHaveJSProperty('paused', true)
                await play.click()
                await Promise.all([
                    expectPlayback(host, hostVideo, 'Host'),
                    expectPlayback(guest, guestVideo, 'Guest'),
                ])
                await pause.click()
                await expect(hostVideo).toHaveJSProperty('paused', true)
                await expect(guestVideo).toHaveJSProperty('paused', true)

                const target = Math.round(duration * 3.5) / 10
                await hostSeek.fill(String(target))
                await expectPosition(hostVideo, target, 'Host seeks to the first position')
                await expectPosition(guestVideo, target, 'Guest follows the first seek')
                return target
            })

        await test.step('Reconnect the guest and receive the current paused snapshot', async () => {
            if (!guestSocket) throw new Error('There is no guest WebSocket to disconnect')

            disconnected = true
            await guestSocket.close({ code: 1000 })
            await expect(
                guestMain.getByText('Восстанавливаем связь…', { exact: true }),
            ).toBeVisible()

            const target = Math.round(duration * 7) / 10
            await hostSeek.fill(String(target))
            await expectPosition(hostVideo, target, 'Host seeks while the guest is disconnected')
            await expectPosition(
                guestVideo,
                firstTarget,
                'Disconnected guest keeps the old position',
            )

            disconnected = false
            await expect(syncStatus(guest)).toBeVisible({ timeout: 15_000 })
            await expectPosition(guestVideo, target, 'Reconnected guest receives the new position')
            await expect(guestVideo).toHaveJSProperty('paused', true)
            await expect(guestMain.getByText('Гость · Вы', { exact: true })).toBeVisible()
            await expectNoHorizontalOverflow(guest, 'Reconnected guest room')
        })

        await test.step('Close the test room', async () => {
            await hostMain.getByRole('button', { name: 'Закрыть комнату', exact: true }).click()
            await hostMain.getByRole('button', { name: 'Завершить просмотр', exact: true }).click()
            await expect(guestMain.getByRole('alert')).toHaveText('Комната закрыта.')
        })
    } finally {
        await guestContext.close()
    }
})

function readSocketMessage(frame: string): z.infer<typeof socketMessageSchema> | undefined {
    try {
        const json: unknown = JSON.parse(frame)
        const parsed = socketMessageSchema.safeParse(json)
        return parsed.success ? parsed.data : undefined
    } catch {
        return undefined
    }
}

function syncStatus(page: Page): Locator {
    return page.getByText('Синхронизация включена', { exact: true })
}

function roomVideo(page: Page): Locator {
    return page.locator('video[aria-label="Видео комнаты"]')
}

async function expectPlayback(page: Page, video: Locator, viewer: string): Promise<void> {
    const initialTime = await video.evaluate((player: HTMLVideoElement) => player.currentTime)
    const activate = page.getByRole('button', { name: 'Включить воспроизведение', exact: true })
    await expect
        .poll(
            async () => {
                if (await activate.isVisible()) await activate.click({ timeout: 2_000 })
                return video.evaluate(
                    (player: HTMLVideoElement, start) =>
                        !player.paused && player.currentTime > start + 0.2,
                    initialTime,
                )
            },
            { message: `${viewer} video starts and advances`, timeout: 12_000 },
        )
        .toBe(true)
}

async function expectPosition(video: Locator, target: number, message: string): Promise<void> {
    await expect
        .poll(
            async () =>
                video.evaluate(
                    (player: HTMLVideoElement, seconds) =>
                        player.seeking ? Infinity : Math.abs(player.currentTime - seconds),
                    target,
                ),
            { message },
        )
        .toBeLessThan(positionTolerance)
}

async function expectNoHorizontalOverflow(page: Page, message: string): Promise<void> {
    await expect
        .poll(
            () =>
                page.evaluate(() => {
                    const root = document.documentElement
                    return Math.max(root.scrollWidth, document.body.scrollWidth) - root.clientWidth
                }),
            { message },
        )
        .toBeLessThanOrEqual(1)
}
