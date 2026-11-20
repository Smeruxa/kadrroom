import { HttpErrorResponse } from '@angular/common/http'
import { Component, inject, signal } from '@angular/core'
import { Router } from '@angular/router'
import { KrButton, KrNotice } from '@kadrroom/ui'
import { OperatorAuth } from './operator-auth'

@Component({
    selector: 'kadrroom-admin-login',
    imports: [KrButton, KrNotice],
    templateUrl: './login-page.html',
    styleUrl: './login-page.scss',
})
export class LoginPage {
    private readonly auth = inject(OperatorAuth)
    private readonly router = inject(Router)
    protected readonly pending = signal(false)
    protected readonly visible = signal(false)
    protected readonly error = signal<string | null>(null)

    protected togglePassword(): void {
        this.visible.update((visible) => !visible)
    }

    protected async login(event: SubmitEvent): Promise<void> {
        event.preventDefault()
        if (this.pending() || !(event.currentTarget instanceof HTMLFormElement)) return

        const fields = new FormData(event.currentTarget)
        const email = fields.get('email')
        const password = fields.get('password')
        if (typeof email !== 'string' || typeof password !== 'string') return

        this.pending.set(true)
        this.error.set(null)
        try {
            await this.auth.login(email.trim(), password)
            await this.router.navigate(['/'], { replaceUrl: true })
        } catch (error) {
            if (error instanceof HttpErrorResponse && error.status === 401) {
                this.error.set('Проверьте почту и пароль.')
            } else if (error instanceof HttpErrorResponse && error.status === 429) {
                this.error.set('Слишком много попыток. Попробуйте чуть позже.')
            } else {
                this.error.set('Не удалось войти. Попробуйте ещё раз.')
            }
        } finally {
            this.pending.set(false)
        }
    }
}
