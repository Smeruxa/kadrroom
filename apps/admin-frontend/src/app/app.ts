import { Component, inject, signal } from '@angular/core'
import { Router, RouterLink, RouterOutlet } from '@angular/router'
import { KrButton, KrNotice } from '@kadrroom/ui'
import { OperatorAuth } from './auth/operator-auth'

@Component({
    selector: 'kadrroom-admin-root',
    imports: [RouterLink, RouterOutlet, KrButton, KrNotice],
    templateUrl: './app.html',
    styleUrl: './app.scss',
})
export class AdminApp {
    private readonly auth = inject(OperatorAuth)
    private readonly router = inject(Router)
    protected readonly account = this.auth.account
    protected readonly waiting = signal(false)
    protected readonly error = signal<string | null>(null)

    constructor() {
        void this.auth.refresh().catch(() => undefined)
    }

    protected async logout(): Promise<void> {
        if (this.waiting()) return

        this.waiting.set(true)
        this.error.set(null)
        try {
            await this.auth.logout()
            await this.router.navigate(['/login'], { replaceUrl: true })
        } catch {
            this.error.set('Не удалось выйти. Повторите попытку.')
        } finally {
            this.waiting.set(false)
        }
    }
}
