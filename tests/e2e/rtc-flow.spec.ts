import { expect, test } from '@playwright/test'
import type { APIResponse, BrowserContext, Page } from '@playwright/test'
import { z } from 'zod'
import { prepareAccount, signInToWeb, useRealStack } from '../support/real-stack'

const getStack = useRealStack('rtc')
const hostName = 'Ведущий тестового звонка'
const guestName = 'Гость тестового звонка'
const rtcMedia = 'last-tram'

const roomResponse = z.object({ roomId: z.string().min(1), invite: z.string().min(1) })
const roomClosedFrame = z.object({ type: z.literal('error'), code: z.literal('room_closed') })

type MediaKind = 'audio' | 'video'
type SignalReplay = 'rejected' | 'reopened' | 'timed_out'

interface InboundMedia {
    audio: number
    video: number
}

declare global {
    interface Window {
        roomInboundMedia(): Promise<InboundMedia>
    }
}

test.skip(({ browserName }) => browserName !== 'chromium', 'Uses Chromium fake media devices')
test.use({
    launchOptions: {
        args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'],
    },
})

test('host and guest exchange live audio and one camera stream', async ({ browser }) => {
    test.setTimeout(90_000)
    const stack = getStack()
    const origin = stack.webOrigin
    const hostContext = await browser.newContext({
        baseURL: origin,
        permissions: ['microphone', 'camera'],
    })
    const guestContext = await browser.newContext({
        baseURL: origin,
        permissions: ['microphone', 'camera'],
    })
    let roomId: string | undefined

    try {
        await Promise.all([observeMedia(hostContext), observeMedia(guestContext)])
        const host = await hostContext.newPage()
        const room = await test.step('Create real host and guest room sessions', async () => {
            const account = await prepareAccount(host, stack, hostName, { emailPrefix: 'rtc-flow' })
            await signInToWeb(host, stack, account)
            const created = await post(hostContext, origin, '/v1/rooms', { mediaId: rtcMedia }, 201)
            const payload: unknown = await created.json()
            const result = roomResponse.safeParse(payload)
            if (!result.success) throw new Error('Create room returned invalid data')

            const room = result.data
            roomId = room.roomId
            await post(
                guestContext,
                origin,
                '/v1/join',
                { invite: room.invite, name: guestName },
                200,
            )
            return room
        })

        const guest = await guestContext.newPage()
        const signalAddress = rememberSignalAddress(guest)
        await keepGuestRoomOpen(guest)
        const roomPath = `/rooms/${encodeURIComponent(room.roomId)}`
        await Promise.all([host.goto(roomPath), guest.goto(roomPath)])
        const hostCall = host.getByRole('region', { name: 'Звонок' })
        const guestCall = guest.getByRole('region', { name: 'Звонок' })
        const hostMic = hostCall.getByRole('button', { name: 'Микрофон', exact: true })
        const guestMic = guestCall.getByRole('button', { name: 'Микрофон', exact: true })
        const hostCamera = hostCall.getByRole('button', { name: 'Камера', exact: true })

        await test.step('Both clients connect without capturing a device', async () => {
            await Promise.all([
                hostCall.getByRole('button', { name: 'Войти в звонок' }).click(),
                guestCall.getByRole('button', { name: 'Войти в звонок' }).click(),
            ])
            await Promise.all([
                expect(hostCall.getByText('Вы в звонке.')).toBeVisible({ timeout: 30_000 }),
                expect(guestCall.getByText('Вы в звонке.')).toBeVisible({ timeout: 30_000 }),
            ])
            await expect(hostMic).toHaveAttribute('aria-pressed', 'false')
            await expect(guestMic).toHaveAttribute('aria-pressed', 'false')
            await expect(hostCamera).toHaveAttribute('aria-pressed', 'false')
            await expect(
                guestCall.getByRole('button', { name: 'Камера', exact: true }),
            ).toHaveAttribute('aria-pressed', 'false')
        })

        const remoteCamera = guestCall
            .locator('figure')
            .filter({ hasText: hostName })
            .locator('video')
        await test.step('Audio reaches each client and a real camera stream is rendered', async () => {
            await hostMic.click()
            await expect(hostMic).toHaveAttribute('aria-pressed', 'true')
            await guestMic.click()
            await expect(guestMic).toHaveAttribute('aria-pressed', 'true')
            await Promise.all([expectInbound(host, 'audio'), expectInbound(guest, 'audio')])

            await hostCamera.click()
            await expect(hostCamera).toHaveAttribute('aria-pressed', 'true')
            await expectInbound(guest, 'video')
            await expect(remoteCamera).toBeVisible()
            await expect
                .poll(() =>
                    remoteCamera.evaluate((element: HTMLVideoElement) => element.videoWidth),
                )
                .toBeGreaterThan(0)
        })

        await test.step('Disabling camera and leaving detach remote tracks', async () => {
            await hostCamera.click()
            await expect(hostCamera).toHaveAttribute('aria-pressed', 'false')
            await expect(remoteCamera).toHaveCount(0)
            await hostCall.getByRole('button', { name: 'Выйти из звонка' }).click()
            await expect(hostCall.getByRole('button', { name: 'Войти в звонок' })).toBeVisible()
            await expect(guestCall.locator('audio')).toHaveCount(0)
            await expect(guestCall.getByText('Вы в звонке.')).toBeVisible()
        })

        await test.step('The server ends the call and rejects its previous credentials', async () => {
            await post(hostContext, origin, `/v1${roomPath}/close`, {}, 204)
            roomId = undefined
            await expect(
                guestCall.getByText('Связь звонка потеряна. Подключитесь снова.'),
            ).toBeVisible({
                timeout: 10_000,
            })

            const address = signalAddress()
            if (!address) throw new Error('No original LiveKit handshake was observed')
            await expect(replaySignalAddress(guest, address)).resolves.toBe('rejected')
        })
    } finally {
        if (roomId) {
            await hostContext.request
                .post(`/v1/rooms/${encodeURIComponent(roomId)}/close`, {
                    headers: { Origin: origin },
                    timeout: 2500,
                })
                .catch(() => undefined)
        }
        await Promise.all([hostContext.close(), guestContext.close()])
    }
})

function rememberSignalAddress(page: Page): () => string | undefined {
    let address: string | undefined
    page.on('websocket', (socket) => {
        if (address) return

        const url = new URL(socket.url())
        if (/^\/rtc(?:\/|$)/.test(url.pathname) && url.searchParams.has('access_token')) {
            address = url.href
        }
    })
    return () => address
}

async function keepGuestRoomOpen(page: Page): Promise<void> {
    await page.routeWebSocket(
        (url) => /^\/v1\/rooms\/[^/]+\/ws$/.test(url.pathname),
        (route) => {
            const server = route.connectToServer()
            server.onMessage((message) => {
                if (!isRoomClosed(message)) route.send(message)
            })
            server.onClose(() => undefined)
        },
    )
}

function isRoomClosed(message: string | Buffer): boolean {
    if (typeof message !== 'string') return false

    try {
        const payload: unknown = JSON.parse(message)
        return roomClosedFrame.safeParse(payload).success
    } catch {
        return false
    }
}

async function replaySignalAddress(page: Page, address: string): Promise<SignalReplay> {
    try {
        return await page.evaluate(
            (target) =>
                new Promise<SignalReplay>((resolve) => {
                    const socket = new WebSocket(target)
                    const timer = window.setTimeout(() => {
                        finish('timed_out')
                    }, 3500)

                    function finish(outcome: SignalReplay): void {
                        window.clearTimeout(timer)
                        socket.onopen = null
                        socket.onerror = null
                        socket.onclose = null
                        if (socket.readyState < WebSocket.CLOSING) socket.close()
                        resolve(outcome)
                    }

                    socket.onopen = () => {
                        finish('reopened')
                    }
                    socket.onerror = () => {
                        finish('rejected')
                    }
                    socket.onclose = () => {
                        finish('rejected')
                    }
                }),
            address,
        )
    } catch {
        throw new Error('Could not test the previous LiveKit connection')
    }
}

async function post(
    context: BrowserContext,
    origin: string,
    path: string,
    data: unknown,
    expectedStatus: number,
): Promise<APIResponse> {
    const response = await context.request.post(path, {
        data,
        headers: { Origin: origin },
        timeout: 8000,
    })
    if (response.status() !== expectedStatus) {
        throw new Error(`${path} returned HTTP ${String(response.status())}`)
    }
    return response
}

async function observeMedia(context: BrowserContext): Promise<void> {
    await context.addInitScript(() => {
        const NativePeer = globalThis.RTCPeerConnection
        const peers: RTCPeerConnection[] = []
        globalThis.RTCPeerConnection = class extends NativePeer {
            constructor(options?: RTCConfiguration) {
                super(options)
                peers.push(this)
            }
        }
        window.roomInboundMedia = async () => {
            const received: InboundMedia = { audio: 0, video: 0 }
            for (const peer of peers) {
                const reports = await peer.getStats()
                reports.forEach((report: RTCInboundRtpStreamStats) => {
                    if (report.type !== 'inbound-rtp') return
                    if (report.kind === 'audio' || report.kind === 'video') {
                        received[report.kind] += report.bytesReceived ?? 0
                    }
                })
            }
            return received
        }
    })
}

async function expectInbound(page: Page, kind: MediaKind): Promise<void> {
    await expect
        .poll(
            async () => {
                const received = await page.evaluate(() => window.roomInboundMedia())
                return received[kind]
            },
            { timeout: 15_000, message: `The remote client receives real ${kind} packets` },
        )
        .toBeGreaterThan(100)
}
