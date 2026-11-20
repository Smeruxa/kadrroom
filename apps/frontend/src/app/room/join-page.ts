import { DOCUMENT } from '@angular/common'
import { HttpErrorResponse } from '@angular/common/http'
import {
    afterNextRender,
    Component,
    ElementRef,
    inject,
    Injector,
    linkedSignal,
    signal,
    viewChild,
} from '@angular/core'
import type { Signal } from '@angular/core'
import { toSignal } from '@angular/core/rxjs-interop'
import { ActivatedRoute, Router, RouterLink } from '@angular/router'
import { KrButton, KrNotice } from '@kadrroom/ui'
import { map } from 'rxjs'
import { apiErrorCode } from '../shared/http/api-error'
import { RoomApi } from './room-api'

interface JoinFailure {
    message: string
    terminal?: true
}

function joinError(error: unknown): JoinFailure {
    const code = apiErrorCode(error)
    if (code === 'invalid_request')
        return { message: 'Проверьте имя, которое увидят участники комнаты.' }
    if (code === 'room_full')
        return { message: 'В комнате уже пять участников. Попробуйте чуть позже.' }
    if (code === 'room_closed') return { message: 'Ведущий уже закрыл комнату.', terminal: true }
    if (code === 'invalid_invite' || code === 'invite_expired') {
        return {
            message:
                'Приглашение недействительно или устарело. Попросите ведущего прислать новую ссылку.',
            terminal: true,
        }
    }
    if (error instanceof HttpErrorResponse && [404, 410].includes(error.status)) {
        return { message: 'Комната или приглашение больше недоступны.', terminal: true }
    }
    return { message: 'Не удалось войти в комнату. Попробуйте ещё раз.' }
}

@Component({
    selector: 'kadrroom-join-page',
    imports: [RouterLink, KrButton, KrNotice],
    templateUrl: './join-page.html',
    styleUrl: './join-page.scss',
})
export class JoinPage {
    private readonly api = inject(RoomApi)
    private readonly router = inject(Router)
    private readonly document = inject(DOCUMENT)
    private readonly route = inject(ActivatedRoute)
    private readonly injector = inject(Injector)
    private readonly guestNameControl = viewChild<ElementRef<HTMLInputElement>>('guestNameControl')
    private readonly invitationControl =
        viewChild<ElementRef<HTMLInputElement>>('invitationControl')
    private readonly replacementControl = viewChild<unknown, ElementRef<HTMLButtonElement>>(
        'replacementControl',
        { read: ElementRef },
    )
    protected readonly invite = toSignal(
        this.route.queryParamMap.pipe(map((params) => params.get('invite'))),
        { initialValue: this.route.snapshot.queryParamMap.get('invite') },
    )
    protected readonly pending = signal(false)
    protected readonly failure = linkedSignal<string | null, JoinFailure | null>({
        source: this.invite,
        computation: () => null,
    })

    protected async openInvitation(event: SubmitEvent): Promise<void> {
        event.preventDefault()
        if (!(event.currentTarget instanceof HTMLFormElement)) return

        const input = new FormData(event.currentTarget).get('invitation')
        const origin = this.document.location.origin
        const link = typeof input === 'string' && URL.canParse(input) ? new URL(input) : null
        const token = link?.searchParams.get('invite') ?? ''
        if (
            link?.origin !== origin ||
            link.pathname !== '/join' ||
            !/^[\w-]{32,128}$/.test(token)
        ) {
            this.failure.set({ message: 'Введите ссылку-приглашение из KadrRoom.' })
            this.invitationControl()?.nativeElement.focus()
            return
        }
        if (
            await this.router.navigate(['/join'], {
                queryParams: { invite: token },
                replaceUrl: true,
            })
        ) {
            this.focusAfterRender(this.guestNameControl)
        }
    }

    protected async replaceInvitation(): Promise<void> {
        if (await this.router.navigate(['/join'], { replaceUrl: true })) {
            this.focusAfterRender(this.invitationControl)
        }
    }

    protected async join(event: SubmitEvent): Promise<void> {
        event.preventDefault()
        const invite = this.invite()
        if (!invite || this.pending() || !(event.currentTarget instanceof HTMLFormElement)) return

        const name = new FormData(event.currentTarget).get('name')
        if (typeof name !== 'string' || !name.trim()) {
            this.failure.set({ message: 'Введите имя, которое увидят участники комнаты.' })
            this.guestNameControl()?.nativeElement.focus()
            return
        }

        this.pending.set(true)
        this.failure.set(null)
        try {
            const room = await this.api.join(invite, name.trim())
            if (this.invite() === invite) {
                await this.router.navigate(['/rooms', room.roomId], { replaceUrl: true })
            }
        } catch (error) {
            if (this.invite() !== invite) return

            const failure = joinError(error)
            this.failure.set(failure)
            if (failure.terminal) this.focusAfterRender(this.replacementControl)
        } finally {
            this.pending.set(false)
        }
    }

    private focusAfterRender(control: Signal<ElementRef<HTMLElement> | undefined>): void {
        afterNextRender(
            { write: () => control()?.nativeElement.focus() },
            { injector: this.injector },
        )
    }
}
