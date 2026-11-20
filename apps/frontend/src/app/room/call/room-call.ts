import {
    ChangeDetectionStrategy,
    Component,
    DestroyRef,
    computed,
    inject,
    input,
    signal,
} from '@angular/core'
import { KrButton, KrIcon } from '@kadrroom/ui'
import type { KrIconName } from '@kadrroom/ui'
import { RemoteAudioTrack, Room, RoomEvent, Track } from 'livekit-client'
import { apiErrorCode } from '../../shared/http/api-error'
import { RoomApi } from '../room-api'
import { CallTrack } from './call-track'

type CallStatus = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'unavailable'
type CallDevice = 'microphone' | 'camera'

interface CallAction {
    icon: KrIconName
    label: string
}

const callActions: Record<CallStatus, CallAction> = {
    idle: { icon: 'phone', label: 'Войти в звонок' },
    connecting: { icon: 'cancel', label: 'Отменить' },
    connected: { icon: 'phone-off', label: 'Выйти из звонка' },
    reconnecting: { icon: 'phone-off', label: 'Выйти из звонка' },
    unavailable: { icon: 'refresh', label: 'Проверить снова' },
}

interface CameraFeed {
    id: string
    name: string
    track: Track
}

interface AudioFeed {
    id: string
    track: RemoteAudioTrack
}

@Component({
    selector: 'kadrroom-room-call',
    imports: [CallTrack, KrButton, KrIcon],
    templateUrl: './room-call.html',
    styleUrl: './room-call.scss',
    changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RoomCall {
    readonly roomId = input.required<string>()

    private readonly api = inject(RoomApi)
    private readonly destroyRef = inject(DestroyRef)
    private room: Room | null = null

    protected readonly status = signal<CallStatus>('idle')
    protected readonly callAction = computed(() => callActions[this.status()])
    protected readonly error = signal<string | null>(null)
    protected readonly microphone = signal(false)
    protected readonly camera = signal(false)
    protected readonly devicePending = signal(false)
    protected readonly audioBlocked = signal(false)
    protected readonly voiceVolume = signal(1)
    protected readonly cameras = signal<CameraFeed[]>([])
    protected readonly voices = signal<AudioFeed[]>([])

    constructor() {
        this.destroyRef.onDestroy(() => {
            this.disconnect()
        })
    }

    protected handleCallAction(): void {
        const status = this.status()
        if (status === 'idle' || status === 'unavailable') {
            void this.join()
        } else {
            this.leave()
        }
    }

    private async join(): Promise<void> {
        if (this.room) return

        this.status.set('connecting')
        this.error.set(null)
        const room = new Room({
            adaptiveStream: true,
            dynacast: true,
            publishDefaults: { stopMicTrackOnMute: true },
        })
        this.room = room
        this.bind(room)

        try {
            const endpoint = await this.api.rtcToken(this.roomId())
            if (this.room !== room) return

            await room.connect(endpoint.url, endpoint.token)
            if (this.room !== room) return

            this.status.set('connected')
            this.refreshTracks(room)
            await this.enableAudio()
        } catch (error) {
            if (this.room !== room) return

            this.disconnect()
            if (apiErrorCode(error) === 'rtc_unavailable') {
                this.status.set('unavailable')
            } else {
                this.status.set('idle')
                this.error.set('Не удалось войти в звонок. Попробуйте ещё раз.')
            }
        }
    }

    private leave(): void {
        this.disconnect()
        this.error.set(null)
        this.status.set('idle')
    }

    protected async toggleDevice(device: CallDevice): Promise<void> {
        const room = this.room
        if (!room || this.status() !== 'connected' || this.devicePending()) return

        this.devicePending.set(true)
        this.error.set(null)
        try {
            const track =
                device === 'microphone'
                    ? await room.localParticipant.setMicrophoneEnabled(!this.microphone())
                    : await room.localParticipant.setCameraEnabled(!this.camera())
            if (this.room !== room) track?.track?.stop()
        } catch {
            if (this.room === room) {
                this.error.set(
                    device === 'microphone'
                        ? 'Микрофон недоступен. Проверьте устройство и разрешение браузера.'
                        : 'Камера недоступна. Проверьте устройство и разрешение браузера.',
                )
            }
        } finally {
            if (this.room === room) {
                this.devicePending.set(false)
                this.refreshTracks(room)
            }
        }
    }

    protected async enableAudio(): Promise<void> {
        const room = this.room
        if (!room) return

        try {
            await room.startAudio()
            if (this.room === room) this.audioBlocked.set(!room.canPlaybackAudio)
        } catch {
            if (this.room === room) this.audioBlocked.set(true)
        }
    }

    protected changeVolume(event: Event): void {
        const field = event.target
        if (!(field instanceof HTMLInputElement)) return

        this.voiceVolume.set(field.valueAsNumber)
        for (const feed of this.voices()) feed.track.setVolume(this.voiceVolume())
    }

    private bind(room: Room): void {
        const refresh = () => {
            this.refreshTracks(room)
        }
        room.on(RoomEvent.TrackSubscribed, refresh)
        room.on(RoomEvent.TrackUnsubscribed, refresh)
        room.on(RoomEvent.TrackMuted, refresh)
        room.on(RoomEvent.TrackUnmuted, refresh)
        room.on(RoomEvent.LocalTrackPublished, refresh)
        room.on(RoomEvent.LocalTrackUnpublished, refresh)
        room.on(RoomEvent.ParticipantDisconnected, refresh)
        room.on(RoomEvent.AudioPlaybackStatusChanged, (playing) => {
            if (this.room === room) this.audioBlocked.set(!playing)
        })
        room.on(RoomEvent.Reconnecting, () => {
            if (this.room === room) this.status.set('reconnecting')
        })
        room.on(RoomEvent.Reconnected, () => {
            if (this.room !== room) return

            this.status.set('connected')
            this.refreshTracks(room)
        })
        room.on(RoomEvent.Disconnected, () => {
            if (this.room !== room || this.status() === 'connecting') return

            this.disconnect()
            this.status.set('idle')
            this.error.set('Связь звонка потеряна. Подключитесь снова.')
        })
    }

    private refreshTracks(room: Room): void {
        if (this.room !== room) return

        const local = room.localParticipant
        this.microphone.set(local.isMicrophoneEnabled)
        this.camera.set(local.isCameraEnabled)

        const cameras: CameraFeed[] = []
        const voices: AudioFeed[] = []
        const localCamera = local.getTrackPublication(Track.Source.Camera)
        if (localCamera?.track && !localCamera.isMuted) {
            cameras.push({ id: localCamera.trackSid, name: 'Вы', track: localCamera.track })
        }
        for (const participant of room.remoteParticipants.values()) {
            const camera = participant.getTrackPublication(Track.Source.Camera)
            if (camera?.track && !camera.isMuted) {
                cameras.push({
                    id: camera.trackSid,
                    name: participant.name || 'Участник',
                    track: camera.track,
                })
            }
            const microphone = participant.getTrackPublication(Track.Source.Microphone)
            if (microphone?.track instanceof RemoteAudioTrack) {
                microphone.track.setVolume(this.voiceVolume())
                voices.push({ id: microphone.trackSid, track: microphone.track })
            }
        }
        this.cameras.set(cameras)
        this.voices.set(voices)
    }

    private disconnect(): void {
        const room = this.room
        this.room = null
        this.microphone.set(false)
        this.camera.set(false)
        this.devicePending.set(false)
        this.audioBlocked.set(false)
        this.cameras.set([])
        this.voices.set([])
        if (room) {
            room.removeAllListeners()
            void room.disconnect().catch(() => undefined)
        }
    }
}
