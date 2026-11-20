import { expect, test } from '@playwright/test'
import type { Locator, Page } from '@playwright/test'
import { z } from 'zod'
import { openPrivateLink, prepareAccount, signInToWeb, useRealStack } from '../support/real-stack'

const film = 'Последний трамвай'
const timeline = 'Позиция воспроизведения'
const backward = 'Назад на 5 секунд'
const forward = 'Вперёд на 5 секунд'
const expand = 'Полный экран'
const collapse = 'Выйти из полноэкранного режима'
const positionTolerance = 0.35
const getStack = useRealStack('web')

test('video room preserves accessible playback controls and shares only host seeks', async ({
    page: host,
    browser,
    browserName,
}) => {
    test.setTimeout(100_000)
    const stack = getStack()
    await host.setViewportSize({ width: 1360, height: 900 })
    const account = await prepareAccount(host, stack, 'Ведущий проверки плеера', {
        emailPrefix: 'player-controls',
    })
    await signInToWeb(host, stack, account)
    const item = host.getByRole('listitem').filter({
        has: host.getByRole('heading', { name: film, exact: true }),
    })
    await button(item, 'Создать комнату').click()
    const invitation = await host.getByLabel('Ссылка для гостя', { exact: true }).evaluate(
        (field: HTMLInputElement) => {
            const value = field.value
            field.value = ''
            return value
        },
        undefined,
        { timeout: 15_000 },
    )
    const roomPath = new URL(host.url()).pathname

    const touchGuest = browserName === 'chromium'
    const guestContext = await browser.newContext({
        viewport: { width: 390, height: 844 },
        hasTouch: touchGuest,
    })
    try {
        const guest = await guestContext.newPage()
        let guestCommands = 0
        guest.on('websocket', (socket) => {
            if (!/^\/v1\/rooms\/[^/]+\/ws$/.test(new URL(socket.url()).pathname)) return
            socket.on('framesent', ({ payload }) => {
                if (typeof payload === 'string' && /"type"\s*:\s*"command"/.test(payload)) {
                    guestCommands++
                }
            })
        })
        await guest.goto(stack.webOrigin)
        await openPrivateLink(guest, invitation, stack.webOrigin, 'join')
        await guest.getByLabel('Имя в комнате', { exact: true }).fill('Гость проверки плеера')
        await button(guest, 'Присоединиться').click()
        await Promise.all(
            [host, guest].map((page) =>
                expect(page.getByText('Синхронизация включена', { exact: true })).toBeVisible({
                    timeout: 15_000,
                }),
            ),
        )

        const hostPlayer = player(host)
        const guestPlayer = player(guest)
        const hostVideo = roomVideo(hostPlayer)
        const guestVideo = roomVideo(guestPlayer)
        const hostSeek = hostPlayer.getByRole('slider', { name: timeline, exact: true })
        const guestProgress = guestPlayer.getByRole('progressbar', { name: timeline, exact: true })
        const play = button(hostPlayer, 'Воспроизвести')
        const pause = button(hostPlayer, 'Пауза')
        const back = button(hostPlayer, backward)
        const ahead = button(hostPlayer, forward)
        await expect(hostSeek).toBeEnabled({ timeout: 15_000 })
        await expect(guestProgress).toBeVisible()
        const duration = await hostVideo.evaluate((video: HTMLVideoElement) => video.duration)
        expect(duration).toBeGreaterThan(15)

        await test.step('Room icons retain accessible names, tooltips and keyboard focus', async () => {
            for (const label of ['Войти в звонок', 'Копировать ссылку', 'Закрыть комнату']) {
                await expect(button(host, label)).toHaveAttribute('title', label)
            }
            await expect(
                guest.getByRole('link', { name: 'Покинуть комнату', exact: true }),
            ).toHaveAttribute('title', 'Покинуть комнату')
            const close = button(host, 'Закрыть комнату')
            await close.press('Enter')
            const cancel = button(host, 'Отмена')
            await expect(button(host, 'Завершить просмотр')).toHaveAttribute(
                'title',
                'Завершить просмотр',
            )
            await expect(cancel).toBeFocused()
            await cancel.press('Enter')
            await expect(close).toBeFocused()
        })

        await test.step('Host transport buttons and Arrow keys synchronize and clamp real media', async () => {
            const controls = hostPlayer.locator('.controls')
            await host.mouse.move(4, 4)
            await expect(hostVideo).toHaveJSProperty('paused', true)
            await expect(controls).toHaveCSS('opacity', '0')
            await hostVideo.hover({ position: { x: 60, y: 70 } })
            await expect(controls).toHaveCSS('opacity', '1')
            for (const [control, label] of [
                [back, backward],
                [ahead, forward],
                [button(hostPlayer, expand), expand],
            ] as const) {
                await expect(control).toHaveAccessibleName(label)
                await expect(control).toHaveAttribute('title', label)
            }
            await expectPosition([hostVideo, guestVideo], 0)
            await back.click()
            await expectPosition([hostVideo, guestVideo], 0)
            await ahead.click()
            await expectPosition([hostVideo, guestVideo], 5)
            await hostPlayer.focus()
            await host.keyboard.press('ArrowRight')
            await expectPosition([hostVideo, guestVideo], 10)
            await hostSeek.press('ArrowLeft')
            await expectPosition([hostVideo, guestVideo], 5)
            await back.click()
            await expectPosition([hostVideo, guestVideo], 0)

            await hostSeek.fill(String(duration - 1))
            await expectPosition([hostVideo, guestVideo], duration - 1)
            await ahead.click()
            await expectPosition([hostVideo, guestVideo], duration)
            await expect
                .poll(() =>
                    guestProgress.evaluate((progress: HTMLProgressElement) =>
                        Math.abs(progress.value - progress.max),
                    ),
                )
                .toBeLessThan(positionTolerance)
        })

        await test.step('A narrow guest keeps local controls without moving the shared timeline', async () => {
            await expect(button(guestPlayer, backward)).toHaveCount(0)
            await expect(button(guestPlayer, forward)).toHaveCount(0)
            await expect(button(guestPlayer, 'Воспроизвести')).toHaveCount(0)
            await expect(guestPlayer.getByRole('slider', { name: timeline })).toHaveCount(0)
            await expect(guestPlayer.getByRole('slider', { name: 'Громкость' })).toHaveCount(0)
            await guestPlayer.focus()
            await guest.keyboard.press('ArrowLeft')
            await guest.keyboard.press('ArrowRight')
            await expectPosition([hostVideo, guestVideo], duration)
            await expectNoHorizontalOverflow(guest)

            await openFullscreen(guestPlayer)
            await expect(guestVideo).toBeVisible()
            await expect(guestProgress).toBeVisible()
            await expect(button(guestPlayer, forward)).toHaveCount(0)
            await expectNoHorizontalOverflow(guest)
            await button(guestPlayer, collapse).click()
            await expect(button(guestPlayer, expand)).toBeVisible()
        })

        await test.step('The inline overlay slides over the frame and the speaker changes local volume', async () => {
            const controls = hostPlayer.locator('.controls')
            const volume = hostPlayer.locator('.volume-track')
            const speaker = button(hostPlayer, 'Выключить звук')
            const volumeSlider = hostPlayer.getByRole('slider', { name: 'Громкость', exact: true })
            await hostVideo.hover({ position: { x: 60, y: 70 } })
            await expect(controls).toHaveCSS('position', 'absolute')
            await expect(speaker).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
            await expect(volume).toHaveCSS('opacity', '0')
            await speaker.hover()
            await expect(volume).toHaveCSS('opacity', '1')
            await hostVideo.hover({ position: { x: 60, y: 70 } })
            await expect(volume).toHaveCSS('opacity', '0')
            await ahead.press('Tab')
            await expect(speaker).toBeFocused()
            await expect(volume).toHaveCSS('opacity', '1')
            await host.keyboard.press('Tab')
            await expect(volumeSlider).toBeFocused()
            await volumeSlider.fill('0.35')
            await expect(hostVideo).toHaveJSProperty('volume', 0.35)
            await expect(guestVideo).toHaveJSProperty('volume', 1)
            await host.mouse.move(4, 4)
            await button(hostPlayer, expand).press('Tab')
            await expect(hostPlayer).not.toBeFocused()
            await expect(hostPlayer.locator(':focus')).toHaveCount(0)
            await expect(controls).toHaveCSS('opacity', '0')

            await hostSeek.fill('0')
            await expectPosition([hostVideo, guestVideo], 0)
            await hostVideo.hover({ position: { x: 60, y: 70 } })
            await play.click()
            await Promise.all([
                expectLocallyPlaying(hostPlayer, hostVideo),
                expectLocallyPlaying(guestPlayer, guestVideo, touchGuest),
            ])
            await blurActiveElement(host)
            await host.mouse.move(4, 4)
            await expect(controls).toHaveCSS('opacity', '0', { timeout: 6_000 })
            await expect(controls).toHaveCSS('pointer-events', 'none')
            const hiddenTop = await top(controls)
            await hostVideo.hover({ position: { x: 100, y: 110 } })
            await expect(controls).toHaveCSS('opacity', '1')
            expect(await top(controls)).toBeLessThan(hiddenTop - 20)

            if (touchGuest) {
                const guestControls = guestPlayer.locator('.controls')
                const guestVolume = guestPlayer.locator('.volume-track')
                const guestVolumeSlider = guestPlayer.getByRole('slider', { name: 'Громкость' })
                await blurActiveElement(guest)
                await expect(guestControls).toHaveCSS('opacity', '0', { timeout: 6_000 })
                await guestVideo.tap()
                await expect(guestControls).toHaveCSS('opacity', '1')
                const showVolume = button(guestPlayer, 'Показать громкость')
                await expect(showVolume).toHaveAttribute('aria-expanded', 'false')
                await showVolume.tap()
                const hideVolume = button(guestPlayer, 'Скрыть громкость')
                await expect(hideVolume).toHaveAttribute('aria-expanded', 'true')
                await expect(guestVolume).toHaveCSS('opacity', '1')
                await expect(guestVolumeSlider).toBeEnabled()
                await expect(guestVideo).toHaveJSProperty('muted', false)
                await hideVolume.tap()
                await expect(showVolume).toHaveAttribute('aria-expanded', 'false')
                await expect(guestVolume).toHaveCSS('opacity', '0')
                await expect(guestVolumeSlider).toHaveCount(0)
            }

            await hostVideo.hover({ position: { x: 60, y: 70 } })
            await pause.click()
            await expect(hostVideo).toHaveJSProperty('paused', true)
            await expect(guestVideo).toHaveJSProperty('paused', true)
        })

        await test.step('The fullscreen player keeps transport and its overlay is recoverable', async () => {
            const controls = hostPlayer.locator('.controls')
            await hostSeek.fill('0')
            await expectPosition([hostVideo, guestVideo], 0)
            await hostVideo.hover({ position: { x: 60, y: 70 } })
            await openFullscreen(hostPlayer)
            await expect(hostVideo).toBeVisible()
            await expect(hostSeek).toBeVisible()
            await expect(play).toBeEnabled()
            await expect(back).toBeVisible()
            await expect(ahead).toBeVisible()

            await play.click()
            await expectLocallyPlaying(hostPlayer, hostVideo)
            await blurActiveElement(host)
            await hostVideo.hover({ position: { x: 60, y: 70 } })
            await expect(controls).toHaveCSS('opacity', '0', { timeout: 6_000 })
            await expect(controls).toHaveCSS('pointer-events', 'none')

            await hostVideo.hover({ position: { x: 100, y: 110 } })
            await expect(controls).toHaveCSS('opacity', '1')
            await expect(controls).toHaveCSS('opacity', '0', { timeout: 6_000 })

            await hostPlayer.focus()
            await expect(hostPlayer).toBeFocused()
            await host.keyboard.press('Tab')
            await expect(hostPlayer.locator('.controls :focus')).toHaveCount(1)
            await expect(controls).toHaveCSS('opacity', '1')
            await pause.click()
            await expect(hostVideo).toHaveJSProperty('paused', true)
            await expect(guestVideo).toHaveJSProperty('paused', true)
            await button(hostPlayer, collapse).click()
            await expect(button(hostPlayer, expand)).toBeVisible()
            expect(guestCommands).toBe(0)
        })

        await test.step('A denied native request uses the same accessible fullscreen surface', async () => {
            await hostPlayer.evaluate((element) => {
                Object.defineProperty(element, 'requestFullscreen', {
                    configurable: true,
                    value: (): Promise<void> =>
                        Promise.reject(
                            new DOMException('Fullscreen policy denied', 'NotAllowedError'),
                        ),
                })
            })
            await hostVideo.hover({ position: { x: 60, y: 70 } })
            await openFullscreen(hostPlayer)
            await expect(hostPlayer).toHaveClass(/\bimmersive\b/)
            await expect(hostVideo).toBeVisible()
            await expect(hostSeek).toBeVisible()
            await expect(play).toBeEnabled()
            await expectMediaRefreshKeepsFocus(host, hostPlayer, hostVideo)
            await host.keyboard.press('Escape')
            await expect(hostPlayer).not.toHaveClass(/\bimmersive\b/)
            await expect(button(hostPlayer, expand)).toBeVisible()
        })
    } finally {
        await guestContext.close()
        if (/^\/rooms\/[^/]+$/.test(roomPath)) {
            const response = await host.request.post(
                stack.webOrigin + '/v1' + roomPath + '/close',
                {
                    headers: { Origin: stack.webOrigin },
                    timeout: 5_000,
                },
            )
            expect(response.status()).toBe(204)
        }
    }
})

function player(page: Page): Locator {
    return page.getByRole('group', { name: 'Плеер комнаты', exact: true })
}

function roomVideo(surface: Locator): Locator {
    return surface.locator('video[aria-label="Видео комнаты"]')
}

function button(surface: Page | Locator, label: string): Locator {
    return surface.getByRole('button', { name: label, exact: true })
}

async function expectLocallyPlaying(
    surface: Locator,
    video: Locator,
    touch = false,
): Promise<void> {
    const activation = button(surface, 'Включить воспроизведение')
    await expect
        .poll(async () => {
            if (await activation.isVisible()) {
                if (touch) await activation.tap({ timeout: 2_000 })
                else await activation.click({ timeout: 2_000 })
            }
            return video.evaluate((element: HTMLVideoElement) => !element.paused)
        })
        .toBe(true)
}

async function blurActiveElement(page: Page): Promise<void> {
    await page.evaluate(() => {
        if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
    })
}

function top(element: Locator): Promise<number> {
    return element.evaluate((target) => target.getBoundingClientRect().top)
}

async function openFullscreen(surface: Locator): Promise<void> {
    await button(surface, expand).click()
    await expect(button(surface, collapse)).toHaveAttribute('title', collapse)
    await expect
        .poll(() =>
            surface.evaluate((element) => {
                const fullscreen =
                    element === document.fullscreenElement ||
                    element.classList.contains('immersive')
                const bounds = element.getBoundingClientRect()
                return (
                    fullscreen &&
                    bounds.width >= window.innerWidth - 1 &&
                    bounds.height >= window.innerHeight - 1
                )
            }),
        )
        .toBe(true)
}

async function expectMediaRefreshKeepsFocus(
    page: Page,
    surface: Locator,
    video: Locator,
): Promise<void> {
    const exit = button(surface, collapse)
    await exit.focus()
    let releaseResponse: (() => void) | undefined
    const responseGate = new Promise<void>((release) => {
        releaseResponse = release
    })
    const sourcePath = (url: URL): boolean =>
        /^\/v1\/rooms\/[^/]+\/playback-url$/.test(url.pathname)
    await page.route(
        sourcePath,
        async (route) => {
            await responseGate
            const response = await route.fetch()
            const source = z.looseObject({ url: z.string() }).safeParse(await response.json())
            if (!source.success) {
                await route.fulfill({ response })
                return
            }
            const refreshed = new URL(source.data.url, page.url())
            refreshed.searchParams.set('player-refresh-test', '1')
            await route.fulfill({ response, json: { ...source.data, url: refreshed.href } })
        },
        { times: 1 },
    )
    const request = page.waitForRequest((item) => sourcePath(new URL(item.url())))
    try {
        await video.dispatchEvent('error')
        await request
        await expect(surface).toHaveClass(/\bimmersive\b/)
        await expect(exit).toBeFocused()
    } finally {
        releaseResponse?.()
    }
    await expect(video).toHaveAttribute('src', /player-refresh-test=1/)
    await expect(surface).toHaveClass(/\bimmersive\b/)
    await expect(exit).toBeFocused()
}

async function expectPosition(videos: Locator[], target: number): Promise<void> {
    await Promise.all(
        videos.map((video) =>
            expect
                .poll(() =>
                    video.evaluate(
                        (element: HTMLVideoElement, seconds) =>
                            element.seeking ? Infinity : Math.abs(element.currentTime - seconds),
                        target,
                    ),
                )
                .toBeLessThan(positionTolerance),
        ),
    )
}

async function expectNoHorizontalOverflow(page: Page): Promise<void> {
    await expect
        .poll(() =>
            page.evaluate(() => {
                const root = document.documentElement
                return Math.max(root.scrollWidth, document.body.scrollWidth) - root.clientWidth
            }),
        )
        .toBeLessThanOrEqual(1)
}
