import type { ElementRef } from '@angular/core'
import { Component, effect, input, output, signal, viewChild } from '@angular/core'
import type { PlaybackSource, PlaybackState } from '@kadrroom/contracts'
import type { PlaybackCommand } from '../room-api'
import { formatTimecode, playbackClockMs, playbackPosition } from './playback-position'

@Component({
    selector: 'kadrroom-playback',
    templateUrl: './room-playback.html',
    styleUrl: './room-playback.scss',
})
export class RoomPlayback {
    readonly source = input.required<PlaybackSource>()
    readonly state = input<PlaybackState | null>(null)
    readonly offsetMs = input<number | null>(null)
    readonly host = input(false)
    readonly canControl = input(false)
    readonly playbackCommand = output<PlaybackCommand>()
    readonly sourceFailed = output()

    private readonly player = viewChild<ElementRef<HTMLMediaElement>>('player')
    private readonly stage = viewChild<ElementRef<HTMLElement>>('stage')
    protected readonly position = signal(0)
    protected readonly duration = signal(0)
    protected readonly preview = signal<number | null>(null)
    protected readonly volume = signal(1)
    protected readonly autoplayBlocked = signal(false)
    protected readonly buffering = signal(false)
    protected readonly playbackError = signal<string | null>(null)
    protected readonly formatTimecode = formatTimecode
    protected readonly supportsFullscreen = document.fullscreenEnabled

    private playPending = false
    private pauseVersion = 0
    private previousSource: string | undefined

    constructor() {
        effect(() => {
            const source = this.source()
            const player = this.player()?.nativeElement
            const state = this.state()
            const offset = this.offsetMs()
            if (this.previousSource !== source.url) {
                this.previousSource = source.url
                this.playbackError.set(null)
            }
            if (player && state) this.applyState(player, state, offset)
        })
    }

    protected updatePosition(): void {
        const player = this.player()?.nativeElement
        if (!player) return

        const state = this.state()
        if (state) this.align(player, state, this.offsetMs())
        this.position.set(player.currentTime)
        this.duration.set(Number.isFinite(player.duration) ? player.duration : 0)
    }

    protected toggle(): void {
        const state = this.state()
        if (!state) return

        this.playbackCommand.emit({ action: state.playing ? 'pause' : 'play' })
    }

    protected seek(event: Event): void {
        if (!(event.currentTarget instanceof HTMLInputElement)) return

        this.preview.set(null)
        const seconds = event.currentTarget.valueAsNumber
        if (Number.isFinite(seconds)) {
            this.playbackCommand.emit({ action: 'seek', positionMs: Math.round(seconds * 1000) })
        }
    }

    protected previewSeek(event: Event): void {
        if (event.currentTarget instanceof HTMLInputElement) {
            this.preview.set(event.currentTarget.valueAsNumber)
        }
    }

    protected changeVolume(event: Event): void {
        const player = this.player()?.nativeElement
        if (!player || !(event.currentTarget instanceof HTMLInputElement)) return

        player.volume = event.currentTarget.valueAsNumber
        this.volume.set(player.volume)
    }

    protected enablePlayback(): void {
        const player = this.player()?.nativeElement
        const state = this.state()
        if (!player || !state) return

        this.autoplayBlocked.set(false)
        this.applyState(player, state, this.offsetMs())
    }

    protected async fullscreen(): Promise<void> {
        const stage = this.stage()?.nativeElement
        if (!stage) return

        try {
            if (document.fullscreenElement) {
                await document.exitFullscreen()
            } else {
                await stage.requestFullscreen()
            }
        } catch {
            this.playbackError.set('Полноэкранный режим недоступен в этом окне.')
        }
    }

    protected mediaReady(): void {
        const player = this.player()?.nativeElement
        if (!player) return

        player.volume = this.volume()
        this.buffering.set(false)
        this.updatePosition()
        const state = this.state()
        if (state) this.applyState(player, state, this.offsetMs())
    }

    protected ended(): void {
        const state = this.state()
        if (this.canControl() && state?.playing) this.playbackCommand.emit({ action: 'pause' })
    }

    private applyState(
        player: HTMLMediaElement,
        state: PlaybackState,
        offsetMs: number | null,
    ): void {
        if (player.readyState < HTMLMediaElement.HAVE_METADATA) return

        const target = this.align(player, state, offsetMs)
        if (!state.playing) {
            this.pauseVersion++
            player.pause()
            this.buffering.set(false)
            this.autoplayBlocked.set(false)
        } else if (
            target !== undefined &&
            (!Number.isFinite(player.duration) || target < player.duration) &&
            player.paused &&
            !this.playPending &&
            !this.autoplayBlocked()
        ) {
            void this.play(player)
        }
    }

    private async play(player: HTMLMediaElement): Promise<void> {
        const beforePause = this.pauseVersion
        this.playPending = true
        try {
            await player.play()
            if (this.pauseVersion === beforePause) this.autoplayBlocked.set(false)
        } catch (error) {
            if (
                this.pauseVersion === beforePause &&
                error instanceof DOMException &&
                error.name === 'NotAllowedError'
            ) {
                this.autoplayBlocked.set(true)
            }
        } finally {
            this.playPending = false
            const latest = this.state()
            if (
                this.pauseVersion !== beforePause &&
                latest?.playing &&
                this.player()?.nativeElement === player
            ) {
                this.applyState(player, latest, this.offsetMs())
            }
        }
    }

    private align(
        player: HTMLMediaElement,
        state: PlaybackState,
        offsetMs: number | null,
    ): number | undefined {
        if (
            player.readyState < HTMLMediaElement.HAVE_METADATA ||
            (state.playing && offsetMs === null)
        )
            return undefined

        const estimated = playbackPosition(state, playbackClockMs() + (offsetMs ?? 0))
        const target = Number.isFinite(player.duration)
            ? Math.min(estimated, player.duration)
            : estimated
        if (
            !player.seeking &&
            Math.abs(player.currentTime - target) > (state.playing ? 0.9 : 0.08)
        ) {
            player.currentTime = target
        }
        return target
    }
}
