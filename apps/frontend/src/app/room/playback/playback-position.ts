import type { PlaybackState } from '@kadrroom/contracts'

export function playbackClockMs(): number {
    return Math.round(performance.timeOrigin + performance.now())
}

export function playbackPosition(state: PlaybackState, serverTimeMs: number): number {
    return playbackPositionMs(state, serverTimeMs) / 1000
}

export function playbackHasJump(previous: PlaybackState, next: PlaybackState): boolean {
    return (
        next.revision > previous.revision &&
        next.anchorPositionMs !== playbackPositionMs(previous, next.anchorServerTimeMs)
    )
}

function playbackPositionMs(state: PlaybackState, serverTimeMs: number): number {
    const elapsed = state.playing ? Math.max(0, serverTimeMs - state.anchorServerTimeMs) : 0
    return state.anchorPositionMs + elapsed
}

export function formatTimecode(seconds: number): string {
    const whole = Math.max(0, Math.floor(seconds))
    const minutes = Math.floor(whole / 60)
    return `${String(minutes)}:${String(whole % 60).padStart(2, '0')}`
}
