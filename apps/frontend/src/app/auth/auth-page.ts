import { HttpErrorResponse } from '@angular/common/http'
import {
    Component,
    DestroyRef,
    Injector,
    afterNextRender,
    inject,
    signal,
    viewChild,
} from '@angular/core'
import type { ElementRef } from '@angular/core'
import { ActivatedRoute, Router, RouterLink } from '@angular/router'
import { KrButton, KrNotice } from '@kadrroom/ui'
import { AuthApi } from './auth-api'
import { apiErrorCode } from '../shared/http/api-error'

type AuthMode = 'login' | 'register' | 'verify' | 'forgot' | 'reset' | 'account'

interface AuthCopy {
    title: string
    description: string
    submit: string
}

const forms: Record<AuthMode, AuthCopy> = {
    login: {
        title: 'Вход в KadrRoom',
        description: 'Войдите, чтобы создать комнату и пригласить друзей.',
        submit: 'Войти',
    },
    register: {
        title: 'Создать аккаунт',
        description: 'Отправим ссылку на почту. По ней вы сможете задать имя и пароль.',
        submit: 'Отправить ссылку',
    },
    verify: {
        title: 'Завершить регистрацию',
        description: 'Выберите имя, которое увидят участники комнаты, и задайте пароль.',
        submit: 'Создать аккаунт',
    },
    forgot: {
        title: 'Восстановить пароль',
        description: 'Введите почту аккаунта, чтобы получить ссылку для восстановления.',
        submit: 'Отправить ссылку',
    },
    reset: {
        title: 'Задать новый пароль',
        description: 'После смены пароля войдите в аккаунт заново.',
        submit: 'Сохранить пароль',
    },
    account: {
        title: 'Аккаунт',
        description: '',
        submit: 'Выйти',
    },
}

function readField(form: FormData, name: string): string {
    const value = form.get(name)
    return typeof value === 'string' ? value : ''
}

@Component({
    selector: 'kadrroom-auth-page',
    imports: [RouterLink, KrButton, KrNotice],
    templateUrl: './auth-page.html',
    styleUrl: './auth-page.scss',
})
export class AuthPage {
    private readonly auth = inject(AuthApi)
    private readonly route = inject(ActivatedRoute)
    private readonly router = inject(Router)
    private readonly destroyRef = inject(DestroyRef)
    private readonly injector = inject(Injector)
    private readonly requestLink = viewChild<ElementRef<HTMLAnchorElement>>('requestLink')

    protected readonly mode: AuthMode = this.route.snapshot.data['mode'] as AuthMode
    protected readonly copy = forms[this.mode]
    protected readonly returnMedia = this.route.snapshot.queryParamMap.get('media')
    protected readonly continuation = this.returnMedia ? { media: this.returnMedia } : null
    private readonly token = this.route.snapshot.queryParamMap.get('token')
    private readonly confirmsLink = this.mode === 'verify' || this.mode === 'reset'
    protected readonly linkError = signal(
        this.confirmsLink && !this.token ? 'Откройте ссылку из письма или запросите новую.' : null,
    )
    protected readonly account = this.auth.account
    protected readonly pending = signal(false)
    protected readonly error = signal<string | null>(null)
    protected readonly complete = signal(false)
    protected readonly passwordVisible = signal(false)
    protected readonly resendReady = signal(false)
    private lastEmail = ''
    private resendTimer: ReturnType<typeof setTimeout> | undefined

    constructor() {
        this.destroyRef.onDestroy(() => {
            clearTimeout(this.resendTimer)
        })
    }

    protected togglePassword(): void {
        this.passwordVisible.update((visible) => !visible)
    }

    protected async submit(event: SubmitEvent): Promise<void> {
        event.preventDefault()
        if (!(event.currentTarget instanceof HTMLFormElement) || this.pending() || this.linkError())
            return

        const form = new FormData(event.currentTarget)
        const email = readField(form, 'email').trim()
        const password = readField(form, 'password')
        this.pending.set(true)
        this.error.set(null)
        try {
            switch (this.mode) {
                case 'login':
                    await this.auth.login({ email, password })
                    await this.router.navigate(['/'], { queryParams: this.continuation })
                    break
                case 'register':
                case 'forgot':
                    await this.sendEmail(email)
                    break
                case 'verify':
                    if (!this.token) return
                    await this.auth.verify({
                        token: this.token,
                        password,
                        displayName: readField(form, 'displayName').trim(),
                    })
                    await this.showCompletion()
                    break
                case 'reset':
                    if (!this.token) return
                    await this.auth.resetPassword({ token: this.token, password })
                    await this.showCompletion()
                    break
                case 'account':
                    await this.auth.logout()
                    await this.router.navigate(['/'])
                    break
            }
        } catch (error) {
            if (this.destroyRef.destroyed) return

            if (
                this.confirmsLink &&
                error instanceof HttpErrorResponse &&
                (apiErrorCode(error) === 'invalid_token' || error.status === 410)
            ) {
                this.linkError.set('Ссылка истекла или уже использована.')
                afterNextRender(
                    { write: () => this.requestLink()?.nativeElement.focus() },
                    { injector: this.injector },
                )
            } else {
                this.error.set(this.errorMessage(error))
            }
        } finally {
            this.pending.set(false)
        }
    }

    protected async resend(): Promise<void> {
        if (!this.resendReady() || this.pending()) return

        this.pending.set(true)
        this.error.set(null)
        try {
            await this.sendEmail(this.lastEmail)
        } catch (error) {
            this.error.set(this.errorMessage(error))
        } finally {
            this.pending.set(false)
        }
    }

    private async sendEmail(email: string): Promise<void> {
        if (this.mode === 'register') {
            await this.auth.register(email)
        } else {
            await this.auth.requestReset(email)
        }
        this.lastEmail = email
        this.complete.set(true)
        this.resendReady.set(false)
        clearTimeout(this.resendTimer)
        this.resendTimer = setTimeout(() => {
            this.resendReady.set(true)
        }, 30_000)
    }

    private async showCompletion(): Promise<void> {
        this.complete.set(true)
        await this.router.navigate([], {
            relativeTo: this.route,
            queryParams: this.continuation,
            replaceUrl: true,
        })
    }

    private errorMessage(error: unknown): string {
        const code = apiErrorCode(error)
        if (error instanceof HttpErrorResponse) {
            if (error.status === 429) return 'Слишком много запросов. Попробуйте чуть позже.'
            if (this.mode === 'login' && error.status === 401) return 'Проверьте почту и пароль.'
            if (code === 'invalid_password') return 'Выберите другой пароль длиной от 15 символов.'
            if (code === 'invalid_display_name')
                return 'Проверьте имя, которое увидят участники комнаты.'
            if (error.status === 400 || error.status === 422) return 'Проверьте введённые данные.'
        }
        return 'Запрос не выполнен. Попробуйте ещё раз.'
    }
}
