import { HttpClient, HttpErrorResponse } from '@angular/common/http'
import { Injectable, inject, signal } from '@angular/core'
import type { AccountSession } from '@kadrroom/contracts'
import { firstValueFrom } from 'rxjs'
import { z } from 'zod'
import { parsePayload } from '../shared/validation/parse-payload'

const sessionSchema = z.object({
    id: z.string().min(1),
    email: z.string().min(1),
    displayName: z.string().min(1),
    role: z.enum(['user', 'operator']),
}) satisfies z.ZodType<AccountSession>

const authURL = '/v1/auth'
const options = { timeout: 6000 }

@Injectable({ providedIn: 'root' })
export class OperatorAuth {
    private readonly http = inject(HttpClient)
    private readonly session = signal<AccountSession | null>(null)
    private revision = 0

    readonly account = this.session.asReadonly()

    async refresh(): Promise<void> {
        const revision = this.revision
        try {
            const payload = await firstValueFrom(
                this.http.get<unknown>(`${authURL}/session`, options),
            )
            if (revision === this.revision) this.session.set(parsePayload(sessionSchema, payload))
        } catch (error) {
            if (revision === this.revision) this.session.set(null)
            if (!(error instanceof HttpErrorResponse && error.status === 401)) throw error
        }
    }

    async login(email: string, password: string): Promise<void> {
        const payload = await firstValueFrom(
            this.http.post<unknown>(`${authURL}/login`, { email, password }, options),
        )
        const account = parsePayload(sessionSchema, payload)
        this.revision++
        this.session.set(account)
    }

    async logout(): Promise<void> {
        await firstValueFrom(this.http.post<null>(`${authURL}/logout`, {}, options))
        this.clear()
    }

    clear(): void {
        this.revision++
        this.session.set(null)
    }
}
