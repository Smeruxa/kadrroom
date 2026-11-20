import { HttpClient, HttpErrorResponse } from '@angular/common/http'
import { Injectable, inject, signal } from '@angular/core'
import type { AccountSession } from '@kadrroom/contracts'
import { firstValueFrom } from 'rxjs'
import { z } from 'zod'
import { parsePayload } from '../shared/validation/parse-payload'

const accountSessionSchema = z.object({
    id: z.string().min(1),
    email: z.string().min(1),
    displayName: z.string().min(1),
    role: z.enum(['user', 'operator']),
}) satisfies z.ZodType<AccountSession>

const accountURL = '/v1/auth'
const options = { timeout: 6000 }

interface LoginInput {
    email: string
    password: string
}

interface VerifiedAccountInput {
    token: string
    password: string
    displayName: string
}

interface ResetPasswordInput {
    token: string
    password: string
}

@Injectable({ providedIn: 'root' })
export class AuthApi {
    private readonly http = inject(HttpClient)
    private readonly session = signal<AccountSession | null | undefined>(undefined)
    private revision = 0

    readonly account = this.session.asReadonly()

    async refresh(): Promise<void> {
        const revision = this.revision
        try {
            const payload = await firstValueFrom(
                this.http.get<unknown>(`${accountURL}/session`, options),
            )
            if (revision === this.revision)
                this.session.set(parsePayload(accountSessionSchema, payload))
        } catch (error) {
            if (revision === this.revision) this.session.set(null)
            if (!(error instanceof HttpErrorResponse && error.status === 401)) throw error
        }
    }

    async register(email: string): Promise<void> {
        await firstValueFrom(this.http.post<null>(`${accountURL}/register`, { email }, options))
    }

    async verify(input: VerifiedAccountInput): Promise<void> {
        await firstValueFrom(this.http.post<null>(`${accountURL}/verify`, input, options))
    }

    async login(input: LoginInput): Promise<void> {
        const payload = await firstValueFrom(
            this.http.post<unknown>(`${accountURL}/login`, input, options),
        )
        const account = parsePayload(accountSessionSchema, payload)
        this.revision++
        this.session.set(account)
    }

    async logout(): Promise<void> {
        await firstValueFrom(this.http.post<null>(`${accountURL}/logout`, {}, options))
        this.revision++
        this.session.set(null)
    }

    async requestReset(email: string): Promise<void> {
        await firstValueFrom(
            this.http.post<null>(`${accountURL}/password/reset-request`, { email }, options),
        )
    }

    async resetPassword(input: ResetPasswordInput): Promise<void> {
        await firstValueFrom(this.http.post<null>(`${accountURL}/password/reset`, input, options))
        this.revision++
        this.session.set(null)
    }
}
