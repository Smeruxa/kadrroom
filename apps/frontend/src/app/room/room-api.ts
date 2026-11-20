import { HttpClient } from '@angular/common/http'
import { Injectable, inject } from '@angular/core'
import type {
    PlaybackSource,
    PlaybackState,
    RoomCommand,
    RoomParticipant,
    RoomSession,
    RtcToken,
    ServerRoomMessage,
} from '@kadrroom/contracts'
import { firstValueFrom } from 'rxjs'
import { z } from 'zod'
import { parsePayload } from '../shared/validation/parse-payload'

const roomRole = z.enum(['host', 'guest'])

const roomSessionSchema = z.object({
    roomId: z.string().min(1),
    participantId: z.string().min(1),
    name: z.string().min(1),
    role: roomRole,
    mediaId: z.string().min(1),
    invite: z.string().min(1).optional(),
}) satisfies z.ZodType<RoomSession>

const roomParticipantSchema = z.object({
    id: z.string().min(1),
    name: z.string().min(1),
    role: roomRole,
}) satisfies z.ZodType<RoomParticipant>

const playbackStateSchema = z.object({
    mediaId: z.string().min(1),
    playing: z.boolean(),
    anchorPositionMs: z.number().nonnegative(),
    anchorServerTimeMs: z.number().nonnegative(),
    revision: z.number().int().nonnegative(),
}) satisfies z.ZodType<PlaybackState>

const playbackSourceSchema = z.object({
    url: z.string().min(1),
    mediaType: z.enum(['video', 'audio']),
}) satisfies z.ZodType<PlaybackSource>

const serverRoomMessageSchema = z.discriminatedUnion('type', [
    z.object({
        type: z.literal('snapshot'),
        state: playbackStateSchema,
        participants: z.array(roomParticipantSchema).optional(),
        hostConnected: z.boolean().optional(),
    }),
    z.object({ type: z.literal('pong'), clientTimeMs: z.number(), serverTimeMs: z.number() }),
    z.object({ type: z.literal('error'), code: z.string() }),
]) satisfies z.ZodType<ServerRoomMessage>

const ticketSchema = z.object({ ticket: z.string().min(1) })
const rtcTokenSchema = z.object({
    url: z.url({ protocol: /^wss?$/ }),
    token: z.string().min(1),
}) satisfies z.ZodType<RtcToken>
const inviteSchema = z.object({ invite: z.string().min(1) })
const options = { timeout: 6000 }

export type PlaybackCommand = Pick<RoomCommand, 'action' | 'positionMs'>

function roomAddress(id: string): string {
    return `/v1/rooms/${encodeURIComponent(id)}`
}

export function parseRoomMessage(message: unknown): ServerRoomMessage | null {
    if (typeof message !== 'string') return null

    try {
        const payload: unknown = JSON.parse(message)
        return parsePayload(serverRoomMessageSchema, payload)
    } catch {
        return null
    }
}

@Injectable({ providedIn: 'root' })
export class RoomApi {
    private readonly http = inject(HttpClient)

    async create(mediaId: string): Promise<RoomSession> {
        const payload = await firstValueFrom(
            this.http.post<unknown>('/v1/rooms', { mediaId }, options),
        )
        return parsePayload(roomSessionSchema, payload)
    }

    async join(invite: string, name: string): Promise<RoomSession> {
        const payload = await firstValueFrom(
            this.http.post<unknown>('/v1/join', { invite, name }, options),
        )
        return parsePayload(roomSessionSchema, payload)
    }

    async session(id: string): Promise<RoomSession> {
        const payload = await firstValueFrom(this.http.get<unknown>(roomAddress(id), options))
        return parsePayload(roomSessionSchema, payload)
    }

    async rotateInvite(id: string): Promise<string> {
        const payload = await firstValueFrom(
            this.http.post<unknown>(`${roomAddress(id)}/invite`, {}, options),
        )
        return parsePayload(inviteSchema, payload).invite
    }

    async close(id: string): Promise<void> {
        await firstValueFrom(this.http.post<null>(`${roomAddress(id)}/close`, {}, options))
    }

    async playbackSource(id: string): Promise<PlaybackSource> {
        const payload = await firstValueFrom(
            this.http.get<unknown>(`${roomAddress(id)}/playback-url`, options),
        )
        return parsePayload(playbackSourceSchema, payload)
    }

    async rtcToken(id: string): Promise<RtcToken> {
        const payload = await firstValueFrom(
            this.http.post<unknown>(`${roomAddress(id)}/rtc-token`, {}, options),
        )
        return parsePayload(rtcTokenSchema, payload)
    }

    async connect(id: string): Promise<WebSocket> {
        const payload = await firstValueFrom(
            this.http.post<unknown>(`${roomAddress(id)}/ws-ticket`, {}, options),
        )
        const endpoint = new URL(`${roomAddress(id)}/ws`, window.location.origin)
        endpoint.protocol = endpoint.protocol === 'https:' ? 'wss:' : 'ws:'
        endpoint.searchParams.set('ticket', parsePayload(ticketSchema, payload).ticket)
        return new WebSocket(endpoint)
    }
}
