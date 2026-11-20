import { HttpClient, HttpErrorResponse } from '@angular/common/http'
import {
    afterNextRender,
    Component,
    DestroyRef,
    ElementRef,
    Injector,
    computed,
    inject,
    signal,
    viewChild,
} from '@angular/core'
import { ActivatedRoute, Router, RouterLink } from '@angular/router'
import type {
    ClientRoomMessage,
    PlaybackSource,
    PlaybackState,
    RoomParticipant,
    RoomSession,
    ServerRoomMessage,
} from '@kadrroom/contracts'
import { KrButton, KrIcon } from '@kadrroom/ui'
import { firstValueFrom } from 'rxjs'
import { catalogSchema } from '../catalog/catalog.schema'
import { apiErrorCode } from '../shared/http/api-error'
import { parsePayload } from '../shared/validation/parse-payload'
import { RoomCall } from './call/room-call'
import { playbackClockMs } from './playback/playback-position'
import { RoomPlayback } from './playback/room-playback'
import { RoomApi, parseRoomMessage } from './room-api'
import type { PlaybackCommand } from './room-api'

type Connection = 'opening' | 'online' | 'reconnecting' | 'ended'

function accessEnded(error: unknown): boolean {
    return error instanceof HttpErrorResponse && [401, 403, 404, 410].includes(error.status)
}

@Component({
    selector: 'kadrroom-room-page',
    imports: [KrButton, KrIcon, RoomCall, RoomPlayback, RouterLink],
    templateUrl: './room-page.html',
    styleUrl: './room-page.scss',
})
export class RoomPage {
    private readonly route = inject(ActivatedRoute)
    private readonly router = inject(Router)
    private readonly destroyRef = inject(DestroyRef)
    private readonly api = inject(RoomApi)
    private readonly http = inject(HttpClient)
    private readonly injector = inject(Injector)
    private readonly closeTrigger = viewChild<unknown, ElementRef<HTMLButtonElement>>(
        'closeTrigger',
        { read: ElementRef },
    )
    private readonly closeCancel = viewChild<unknown, ElementRef<HTMLButtonElement>>(
        'closeCancel',
        {
            read: ElementRef,
        },
    )
    private readonly id = this.route.snapshot.paramMap.get('id') ?? ''

    protected readonly room = signal<RoomSession | null>(null)
    protected readonly title = signal('Комната KadrRoom')
    protected readonly snapshot = signal<PlaybackState | null>(null)
    protected readonly participants = signal<RoomParticipant[]>([])
    protected readonly hostConnected = signal<boolean | undefined>(undefined)
    protected readonly connection = signal<Connection>('opening')
    protected readonly clockOffset = signal<number | null>(null)
    protected readonly source = signal<PlaybackSource | null>(null)
    protected readonly loading = signal(true)
    protected readonly error = signal<string | null>(null)
    protected readonly sourceError = signal<string | null>(null)
    protected readonly invite = signal<string | null>(null)
    protected readonly inviteNotice = signal<string | null>(null)
    protected readonly commandPending = signal(false)
    protected readonly invitePending = signal(false)
    protected readonly closePending = signal(false)
    protected readonly confirmClose = signal(false)
    protected readonly inviteURL = computed(() => {
        const invite = this.invite()
        if (!invite) return null

        const url = new URL('/join', window.location.origin)
        url.searchParams.set('invite', invite)
        return url.href
    })

    protected readonly canControl = computed(
        () =>
            this.room()?.role === 'host' &&
            this.connection() === 'online' &&
            !this.commandPending(),
    )

    private connecting = false
    private sourcePending = false
    private refreshedAfterError = false
    private socket: WebSocket | null = null
    private pendingRevision: number | null = null
    private attempt = 0
    private bestRoundTrip = Infinity
    private reconnectTimer: ReturnType<typeof setTimeout> | undefined
    private openTimer: ReturnType<typeof setTimeout> | undefined
    private commandTimer: ReturnType<typeof setTimeout> | undefined
    private pingTimer: ReturnType<typeof setInterval> | undefined

    constructor() {
        const initialInvite = this.router.currentNavigation()?.extras.info
        if (typeof initialInvite === 'string') this.invite.set(initialInvite)
        this.destroyRef.onDestroy(() => {
            this.stopConnection()
        })
        void this.loadRoom()
    }

    protected setCloseConfirmation(visible: boolean): void {
        this.confirmClose.set(visible)
        const target = visible ? this.closeCancel : this.closeTrigger
        afterNextRender(
            { write: () => target()?.nativeElement.focus() },
            { injector: this.injector },
        )
    }

    protected async loadRoom(): Promise<void> {
        this.loading.set(true)
        this.error.set(null)
        try {
            const room = await this.api.session(this.id)
            if (this.unavailable()) return

            this.room.set(room)
            this.participants.set([{ id: room.participantId, name: room.name, role: room.role }])
            if (room.role !== 'host') this.invite.set(null)
            if (room.invite) this.invite.set(room.invite)
            void this.loadTitle(room.mediaId)
            void this.loadSource()
            void this.connect()
        } catch (error) {
            if (accessEnded(error)) {
                this.endRoom(
                    apiErrorCode(error) === 'room_closed'
                        ? 'Комната закрыта.'
                        : 'Комната недоступна.',
                )
            } else {
                this.error.set('Не удалось загрузить комнату. Попробуйте ещё раз.')
            }
        } finally {
            this.loading.set(false)
        }
    }

    protected async loadSource(): Promise<void> {
        if (this.sourcePending || this.unavailable()) return

        this.sourcePending = true
        this.sourceError.set(null)
        try {
            const source = await this.api.playbackSource(this.id)
            if (!this.unavailable()) this.source.set(source)
        } catch (error) {
            if (this.unavailable()) return

            if (accessEnded(error) && apiErrorCode(error) !== 'media_unavailable') {
                this.endRoom('Комната закрыта или сеанс истёк.')
            } else {
                this.source.set(null)
                this.sourceError.set('Не удалось загрузить медиа.')
            }
        } finally {
            this.sourcePending = false
        }
    }

    protected onMediaError(failedSource: PlaybackSource): void {
        if (this.sourcePending || this.unavailable() || this.source() !== failedSource) return

        if (!this.refreshedAfterError) {
            this.refreshedAfterError = true
            void this.loadSource()
        } else {
            this.source.set(null)
            this.sourceError.set('Воспроизведение прервалось. Загрузите медиа заново.')
        }
    }

    protected retrySource(): void {
        this.refreshedAfterError = false
        void this.loadSource()
    }

    protected sendCommand(action: PlaybackCommand): void {
        const state = this.snapshot()
        if (!state || !this.canControl()) return

        if (
            this.send({
                type: 'command',
                ...action,
                commandId: globalThis.crypto.randomUUID(),
                expectedRevision: state.revision,
            })
        ) {
            this.pendingRevision = state.revision
            this.commandPending.set(true)
            this.commandTimer = setTimeout(() => {
                this.error.set('Синхронизируем состояние комнаты…')
                this.socket?.close()
            }, 6000)
        }
    }

    protected async copyInvite(field: HTMLInputElement): Promise<void> {
        const link = this.inviteURL()
        if (!link) return

        try {
            await navigator.clipboard.writeText(link)
            this.inviteNotice.set('Ссылка скопирована.')
        } catch {
            field.focus()
            field.select()
            this.inviteNotice.set('Выделили ссылку. Скопируйте её вручную.')
        }
    }

    protected async rotateInvite(): Promise<void> {
        if (this.invitePending()) return

        this.invitePending.set(true)
        this.inviteNotice.set(null)
        try {
            this.invite.set(await this.api.rotateInvite(this.id))
            this.inviteNotice.set('Новая ссылка готова.')
        } catch {
            this.inviteNotice.set('Не удалось создать ссылку. Попробуйте ещё раз.')
        } finally {
            this.invitePending.set(false)
        }
    }

    protected async closeRoom(): Promise<void> {
        if (this.closePending()) return

        this.closePending.set(true)
        try {
            await this.api.close(this.id)
            this.endRoom('Комната закрыта.')
        } catch {
            this.error.set('Не удалось закрыть комнату. Попробуйте ещё раз.')
        } finally {
            this.closePending.set(false)
        }
    }

    private async loadTitle(mediaId: string): Promise<void> {
        try {
            const payload = await firstValueFrom(
                this.http.get<unknown>('/v1/catalog', { timeout: 6000 }),
            )
            const item = parsePayload(catalogSchema, payload).items.find(
                (entry) => entry.id === mediaId,
            )
            if (item && !this.unavailable()) this.title.set(item.title)
        } catch {
            this.title.set('Комната KadrRoom')
        }
    }

    private async connect(): Promise<void> {
        if (this.unavailable() || this.connecting || this.socket) return

        this.connecting = true
        try {
            const socket = await this.api.connect(this.id)
            if (this.unavailable()) {
                socket.close()
                return
            }

            this.socket = socket
            this.openTimer = setTimeout(() => {
                socket.close()
            }, 8000)
            socket.addEventListener('open', () => {
                if (this.socket !== socket) return

                this.bestRoundTrip = Infinity
                this.ping()
                this.pingTimer = setInterval(() => {
                    this.ping()
                }, 5000)
            })
            socket.addEventListener('message', (event: MessageEvent<unknown>) => {
                if (this.socket !== socket) return

                const message = parseRoomMessage(event.data)
                if (message) this.onMessage(message)
            })
            socket.addEventListener('close', () => {
                if (this.socket !== socket) return

                this.stopConnection()
                this.scheduleReconnect()
            })
        } catch (error) {
            if (accessEnded(error)) {
                this.endRoom(
                    apiErrorCode(error) === 'room_closed'
                        ? 'Комната закрыта.'
                        : 'Сеанс комнаты истёк.',
                )
            } else {
                this.scheduleReconnect()
            }
        } finally {
            this.connecting = false
        }
    }

    private onMessage(message: ServerRoomMessage): void {
        switch (message.type) {
            case 'snapshot':
                clearTimeout(this.openTimer)
                this.attempt = 0
                this.connection.set('online')
                if (message.participants) this.participants.set(message.participants)
                this.hostConnected.set(message.hostConnected)
                if ((this.snapshot()?.revision ?? -1) <= message.state.revision) {
                    if (
                        this.pendingRevision !== null &&
                        message.state.revision > this.pendingRevision
                    ) {
                        this.clearCommand()
                    }
                    this.snapshot.set(message.state)
                    this.error.set(null)
                }
                break
            case 'pong': {
                const received = playbackClockMs()
                const roundTrip = received - message.clientTimeMs
                if (roundTrip < 0 || roundTrip > this.bestRoundTrip + 50) return

                this.bestRoundTrip = Math.min(this.bestRoundTrip, roundTrip)
                this.clockOffset.set(message.serverTimeMs - (message.clientTimeMs + received) / 2)
                break
            }
            case 'error':
                this.clearCommand()
                if (message.code === 'room_closed' || message.code === 'room_not_found') {
                    this.endRoom('Комната закрыта.')
                } else if (message.code === 'not_authenticated') {
                    this.endRoom('Сеанс комнаты истёк. Войдите в аккаунт и откройте комнату снова.')
                } else if (message.code === 'upstream_unavailable') {
                    this.error.set('Проверка аккаунта временно недоступна. Попробуйте ещё раз.')
                } else if (message.code === 'stale_revision') {
                    this.socket?.close()
                } else {
                    this.error.set('Не удалось изменить воспроизведение. Попробуйте ещё раз.')
                }
                break
        }
    }

    private ping(): void {
        this.send({ type: 'ping', clientTimeMs: playbackClockMs() })
    }

    private send(message: ClientRoomMessage): boolean {
        if (this.socket?.readyState !== WebSocket.OPEN) return false

        this.socket.send(JSON.stringify(message))
        return true
    }

    private unavailable(): boolean {
        return this.destroyRef.destroyed || this.connection() === 'ended'
    }

    private scheduleReconnect(): void {
        if (this.unavailable()) return

        this.connection.set('reconnecting')
        clearTimeout(this.reconnectTimer)
        const timeout = Math.min(5000, 500 * 2 ** this.attempt)
        this.attempt++
        this.reconnectTimer = setTimeout(() => void this.connect(), timeout)
    }

    private clearCommand(): void {
        clearTimeout(this.commandTimer)
        this.pendingRevision = null
        this.commandPending.set(false)
    }

    private stopConnection(): void {
        clearInterval(this.pingTimer)
        clearTimeout(this.openTimer)
        clearTimeout(this.reconnectTimer)
        this.clearCommand()
        const socket = this.socket
        this.socket = null
        socket?.close()
    }

    private endRoom(note: string): void {
        this.connection.set('ended')
        this.stopConnection()
        this.source.set(null)
        this.invite.set(null)
        this.error.set(note)
    }
}
