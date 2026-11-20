import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { PlaybackState } from '@kadrroom/contracts'
import { playbackHasJump } from '../src/app/room/playback/playback-position.ts'

const playing: PlaybackState = {
    mediaId: 'last-tram',
    playing: true,
    anchorPositionMs: 5000,
    anchorServerTimeMs: 10000,
    revision: 4,
}

await test('an explicit short backward seek jumps even within the normal playback tolerance', () => {
    assert.equal(
        playbackHasJump(playing, {
            ...playing,
            anchorPositionMs: 6400,
            anchorServerTimeMs: 12000,
            revision: 5,
        }),
        true,
    )
})

await test('continuous pause and repeated snapshots keep their existing playback position', () => {
    assert.equal(
        playbackHasJump(playing, {
            ...playing,
            playing: false,
            anchorPositionMs: 7000,
            anchorServerTimeMs: 12000,
            revision: 5,
        }),
        false,
    )
    assert.equal(playbackHasJump(playing, { ...playing, anchorPositionMs: 6000 }), false)
})
