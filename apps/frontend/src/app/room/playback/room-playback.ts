import type { ElementRef } from '@angular/core'
import {
    Component,
    computed,
    DestroyRef,
    effect,
    inject,
    input,
    output,
    signal,
    untracked,
    viewChild,
} from '@angular/core'
import type { PlaybackSource, PlaybackState } from '@kadrroom/contracts'
import { KrIcon } from '@kadrroom/ui'
import type { PlaybackCommand } from '../room-api'
import { PlaybackFullscreen } from './playback-fullscreen'
import {
    formatTimecode,
    playbackClockMs,
    playbackHasJump,
    playbackPosition,
} from './playback-position'

const controlsHideDelayMs = 2600

@Component({
    selector: 'kadrroom-playback',
    imports: [KrIcon, PlaybackFullscreen],
    templateUrl: './room-playback.html',
    styleUrl: './room-playback.scss',
    host: {
        '[style.min-block-size.px]': 'fullscreen()?.placeholderHeight()',
        '(document:pointerup)': 'endScrub($event)',
        '(document:pointercancel)': 'cancelScrub($event)',
    },
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
    private readonly frame = viewChild<ElementRef<HTMLElement>>('frame')
    private readonly timeline = viewChild<ElementRef<HTMLInputElement>>('timeline')
    protected readonly fullscreen = viewChild(PlaybackFullscreen)
    private readonly destroyRef = inject(DestroyRef)
    private readonly mediaPlaying = signal(false)
    private readonly pointerInside = signal(false)
    protected readonly position = signal(0)
    protected readonly duration = signal(0)
    protected readonly preview = signal<number | null>(null)
    protected readonly volume = signal(1)
    protected readonly muted = signal(false)
    protected readonly volumeExpanded = signal(false)
    protected readonly touchControls = signal(matchMedia('(hover: none)').matches)
    protected readonly autoplayBlocked = signal(false)
    protected readonly buffering = signal(false)
    protected readonly formatTimecode = formatTimecode
    protected readonly controlsHidden = signal(!this.touchControls())
    protected readonly playedPercent = computed(() => {
        const duration = this.duration()
        const position = this.preview() ?? this.position()
        return duration > 0 ? Math.min(100, (position / duration) * 100) : 0
    })
    protected readonly canSkip = computed(() => {
        const state = this.state()
        return Boolean(
            this.canControl() &&
            this.duration() > 0 &&
            state &&
            (!state.playing || this.offsetMs() !== null),
        )
    })

    private playPending = false
    private pauseVersion = 0
    private currentSource: PlaybackSource | undefined
    private alignedState: PlaybackState | null = null
    private scrubPointer: number | undefined
    private controlsTimer: ReturnType<typeof setTimeout> | undefined

    constructor() {
        effect(() => {
            const source = this.source()
            const player = this.player()?.nativeElement
            const state = this.state()
            const offset = this.offsetMs()
            if (!player) return

            if (this.currentSource !== source) {
                if (
                    this.currentSource?.url === source.url &&
                    this.currentSource.mediaType === source.mediaType
                ) {
                    player.load()
                }
                this.currentSource = source
            }
            if (state) this.applyState(player, state, offset)
        })
        effect(() => {
            const visibleWithoutFocus = this.visibleWithoutFocus()
            const canAutohide = this.canAutohide()
            untracked(() => {
                if (!visibleWithoutFocus) this.hideControls()
                else if (!canAutohide) this.controlsHidden.set(false)
                this.scheduleHide()
            })
        })
        this.destroyRef.onDestroy(() => {
            clearTimeout(this.controlsTimer)
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
        if (!state || !this.canControl()) return

        this.playbackCommand.emit({ action: state.playing ? 'pause' : 'play' })
    }

    protected seekBy(seconds: number): void {
        const state = this.state()
        const offset = this.offsetMs()
        if (!state || !this.canSkip()) return

        const serverTime = offset === null ? state.anchorServerTimeMs : playbackClockMs() + offset
        const current = playbackPosition(state, serverTime)
        this.seekTo(current + seconds)
    }

    protected seek(event: Event): void {
        if (!(event.currentTarget instanceof HTMLInputElement)) return

        this.preview.set(null)
        if (!this.seekTo(event.currentTarget.valueAsNumber)) {
            event.currentTarget.valueAsNumber = this.position()
        }
    }

    protected previewSeek(event: Event): void {
        if (!(event.currentTarget instanceof HTMLInputElement)) return

        if (this.canControl()) this.preview.set(event.currentTarget.valueAsNumber)
        else event.currentTarget.valueAsNumber = this.position()
    }

    protected beginScrub(event: PointerEvent): void {
        if (event.currentTarget === this.timeline()?.nativeElement && !this.canControl()) return

        this.scrubPointer = event.pointerId
        this.revealControls()
    }

    protected endScrub(event: PointerEvent): void {
        if (event.pointerId !== this.scrubPointer) return

        this.scrubPointer = undefined
        if (this.visibleWithoutFocus()) this.scheduleHide()
        else this.hideControls()
    }

    protected cancelScrub(event: PointerEvent): void {
        if (event.pointerId !== this.scrubPointer) return

        this.preview.set(null)
        this.endScrub(event)
    }

    protected changeVolume(event: Event): void {
        const player = this.player()?.nativeElement
        if (!player || !(event.currentTarget instanceof HTMLInputElement)) return

        player.volume = event.currentTarget.valueAsNumber
        player.muted = false
        this.volume.set(player.volume)
        this.muted.set(player.muted)
    }

    protected toggleVolume(): void {
        if (this.touchControls()) {
            this.volumeExpanded.update((open) => !open)
            return
        }

        const player = this.player()?.nativeElement
        if (!player) return

        const silent = player.muted || player.volume === 0
        if (player.volume === 0) {
            player.volume = 1
            this.volume.set(player.volume)
        }
        player.muted = !silent
        this.muted.set(player.muted)
    }

    protected revealVolume(event: FocusEvent): void {
        if (event.target instanceof HTMLElement && event.target.matches(':focus-visible')) {
            this.volumeExpanded.set(true)
        }
    }

    protected leaveVolume(event: FocusEvent): void {
        if (
            !(event.currentTarget instanceof HTMLElement) ||
            !(event.relatedTarget instanceof Node) ||
            !event.currentTarget.contains(event.relatedTarget)
        ) {
            this.volumeExpanded.set(false)
        }
    }

    protected enablePlayback(): void {
        const player = this.player()?.nativeElement
        const state = this.state()
        if (!player || !state) return

        this.autoplayBlocked.set(false)
        this.applyState(player, state, this.offsetMs())
    }

    protected revealControls(): void {
        this.controlsHidden.set(false)
        this.scheduleHide()
    }

    protected pointerActivated(event: PointerEvent): void {
        this.touchControls.set(event.pointerType === 'touch')
        if (
            !this.touchControls() ||
            !(event.target instanceof Element) ||
            !event.target.closest('.volume')
        ) {
            this.volumeExpanded.set(false)
        }
        this.revealControls()
    }

    protected pointerEntered(event: PointerEvent): void {
        if (event.pointerType === 'touch') return

        this.pointerInside.set(true)
        this.revealControls()
    }

    protected pointerLeft(event: PointerEvent): void {
        if (event.pointerType === 'touch') return

        this.pointerInside.set(false)
        if (!this.visibleWithoutFocus() || this.canAutohide()) this.hideControls()
    }

    protected focusLeft(event: FocusEvent): void {
        if (
            event.relatedTarget instanceof Node &&
            this.frame()?.nativeElement.contains(event.relatedTarget)
        ) {
            return
        }

        if (this.visibleWithoutFocus()) this.scheduleHide()
        else this.hideControls()
    }

    protected handlePlayerKey(event: KeyboardEvent): void {
        this.revealControls()
        if (
            !this.host() ||
            event.altKey ||
            event.ctrlKey ||
            event.metaKey ||
            event.shiftKey ||
            (event.target instanceof HTMLInputElement &&
                event.target !== this.timeline()?.nativeElement)
        )
            return

        if (event.key === ' ' && event.target === this.frame()?.nativeElement) {
            event.preventDefault()
            if (!event.repeat) this.toggle()
        } else if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
            event.preventDefault()
            this.seekBy(event.key === 'ArrowLeft' ? -5 : 5)
        }
    }

    protected mediaReady(): void {
        const player = this.player()?.nativeElement
        if (!player) return

        player.volume = this.volume()
        player.muted = this.muted()
        this.buffering.set(false)
        this.alignedState = null
        this.updatePosition()
        const state = this.state()
        if (state) this.applyState(player, state, this.offsetMs())
    }

    protected mediaStarted(): void {
        this.buffering.set(false)
        this.mediaPlaying.set(true)
    }

    protected mediaStopped(waiting = false): void {
        this.buffering.set(waiting)
        this.mediaPlaying.set(false)
    }

    protected ended(): void {
        this.mediaStopped()
        const state = this.state()
        if (this.canControl() && state?.playing) this.playbackCommand.emit({ action: 'pause' })
    }

    private seekTo(seconds: number): boolean {
        const duration = this.duration()
        if (!this.canControl() || duration <= 0 || !Number.isFinite(seconds)) return false

        const positionMs = Math.round(Math.max(0, Math.min(seconds, duration)) * 1000)
        this.playbackCommand.emit({ action: 'seek', positionMs })
        return true
    }

    private visibleWithoutFocus(): boolean {
        return Boolean(this.fullscreen()?.mode() || this.touchControls() || this.pointerInside())
    }

    private canAutohide(): boolean {
        return Boolean(this.state()?.playing && this.mediaPlaying() && this.preview() === null)
    }

    private scheduleHide(): void {
        clearTimeout(this.controlsTimer)
        if (!this.canAutohide() || this.scrubPointer !== undefined || this.hasKeyboardFocus()) {
            return
        }

        this.controlsTimer = setTimeout(() => {
            const hovered =
                !this.touchControls() &&
                this.frame()?.nativeElement.querySelector('.controls:hover')
            if (this.canAutohide() && !hovered) {
                this.hideControls()
            }
        }, controlsHideDelayMs)
    }

    private hideControls(): void {
        if (this.scrubPointer !== undefined || this.hasKeyboardFocus()) return

        clearTimeout(this.controlsTimer)
        this.controlsHidden.set(true)
        this.volumeExpanded.set(false)
    }

    private hasKeyboardFocus(): boolean {
        const focused = document.activeElement
        return Boolean(
            focused?.matches(':focus-visible') && this.frame()?.nativeElement.contains(focused),
        )
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
        const jumped = !this.alignedState || playbackHasJump(this.alignedState, state)
        this.alignedState = state
        const tolerance = jumped ? 0.001 : state.playing ? 0.9 : 0.08
        if ((!player.seeking || jumped) && Math.abs(player.currentTime - target) > tolerance) {
            player.currentTime = target
        }
        return target
    }
}
