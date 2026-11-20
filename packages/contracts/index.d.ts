export interface CatalogItem {
    id: string
    title: string
    description: string
    durationSeconds: number
    available: boolean
    mediaType?: MediaType | undefined
    coverUrl?: string | undefined
}

export interface CatalogResponse {
    items: CatalogItem[]
}

export type MediaType = 'video' | 'audio'

export interface AccountSession {
    id: string
    email: string
    displayName: string
    role: 'user' | 'operator'
}

export type RoomRole = 'host' | 'guest'

export interface RoomSession {
    roomId: string
    participantId: string
    name: string
    role: RoomRole
    mediaId: string
    invite?: string | undefined
}

export interface RoomParticipant {
    id: string
    name: string
    role: RoomRole
}

export interface PlaybackState {
    mediaId: string
    playing: boolean
    anchorPositionMs: number
    anchorServerTimeMs: number
    revision: number
}

export interface PlaybackSource {
    url: string
    mediaType: MediaType
}

export interface RoomCommand {
    type: 'command'
    action: 'play' | 'pause' | 'seek'
    positionMs?: number | undefined
    commandId: string
    expectedRevision: number
}

export interface RoomPing {
    type: 'ping'
    clientTimeMs: number
}

export type ClientRoomMessage = RoomCommand | RoomPing

export interface RoomSnapshot {
    type: 'snapshot'
    state: PlaybackState
    participants?: RoomParticipant[] | undefined
    hostConnected?: boolean | undefined
}

export interface RtcToken {
    url: string
    token: string
}

export interface RoomMessageError {
    type: 'error'
    code: string
}

export interface RoomPong {
    type: 'pong'
    clientTimeMs: number
    serverTimeMs: number
}

export type ServerRoomMessage = RoomSnapshot | RoomMessageError | RoomPong
