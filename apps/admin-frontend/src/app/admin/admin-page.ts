import { HttpErrorResponse } from '@angular/common/http'
import { Component, DestroyRef, inject, signal } from '@angular/core'
import { takeUntilDestroyed } from '@angular/core/rxjs-interop'
import { Router, RouterLink } from '@angular/router'
import { KrButton, KrNotice } from '@kadrroom/ui'
import { firstValueFrom } from 'rxjs'
import { OperatorAuth } from '../auth/operator-auth'
import { formatDuration } from '../shared/formatting/format-duration'
import { apiErrorCode } from '../shared/http/api-error'
import { AdminApi } from './admin-api'
import type { AdminAction, AdminItem } from './admin-api'
import { validateUpload } from './upload-form'

type AdminAccess = 'checking' | 'allowed' | 'unauthenticated' | 'forbidden' | 'unavailable'

const statusLabels: Record<AdminItem['status'], string> = {
    processing: 'Обрабатывается',
    ready: 'Готово к публикации',
    failed: 'Ошибка обработки',
    published: 'Опубликовано',
}

const updateLabels: Record<AdminAction, string> = {
    publish: 'Запись опубликована.',
    unpublish: 'Запись снята с публикации.',
    retry: 'Запись отправлена на повторную обработку.',
}

function errorMessage(error: unknown): string {
    switch (apiErrorCode(error)) {
        case 'invalid_upload':
            return 'Проверьте название, тип и формат файлов.'
        case 'file_too_large':
            return 'Проверьте размер файлов. Медиа — до 128 МиБ, обложка — до 5 МиБ.'
        case 'media_not_ready':
            return 'Состояние записи изменилось. Обновите список и повторите действие.'
        case 'not_found':
            return 'Запись больше недоступна. Обновите список.'
        case 'invalid_origin':
            return 'Сервер отклонил запрос. Перезагрузите страницу и повторите действие.'
        default:
            return 'Запрос не выполнен. Попробуйте ещё раз.'
    }
}

@Component({
    selector: 'kadrroom-admin-page',
    imports: [RouterLink, KrButton, KrNotice],
    templateUrl: './admin-page.html',
    styleUrl: './admin-page.scss',
})
export class AdminPage {
    private readonly api = inject(AdminApi)
    private readonly auth = inject(OperatorAuth)
    private readonly router = inject(Router)
    private readonly destroyRef = inject(DestroyRef)
    private revision = 0
    private pollTimer: ReturnType<typeof setTimeout> | undefined

    protected readonly access = signal<AdminAccess>('checking')
    protected readonly items = signal<AdminItem[]>([])
    protected readonly refreshing = signal(false)
    protected readonly uploading = signal(false)
    protected readonly mediaName = signal('')
    protected readonly coverName = signal('')
    protected readonly percent = signal<number | null>(null)
    protected readonly activeItem = signal<string | null>(null)
    protected readonly listError = signal<string | null>(null)
    protected readonly uploadError = signal<string | null>(null)
    protected readonly actionError = signal<string | null>(null)
    protected readonly feedback = signal<string | null>(null)
    protected readonly statusLabels = statusLabels
    protected readonly formatDuration = formatDuration

    constructor() {
        this.destroyRef.onDestroy(() => {
            clearTimeout(this.pollTimer)
        })
        void this.refresh()
    }

    protected async refresh(): Promise<void> {
        if (this.refreshing()) return

        this.refreshing.set(true)
        clearTimeout(this.pollTimer)
        const revision = this.revision
        try {
            const items = await firstValueFrom(
                this.api.list().pipe(takeUntilDestroyed(this.destroyRef)),
            )
            if (revision !== this.revision) return

            this.access.set('allowed')
            this.listError.set(null)
            this.items.set(items)
        } catch (error) {
            if (this.destroyRef.destroyed || revision !== this.revision || this.checkAccess(error))
                return

            if (this.access() === 'allowed') {
                this.listError.set('Не удалось обновить список записей.')
            } else {
                this.access.set('unavailable')
            }
        } finally {
            this.refreshing.set(false)
            this.schedulePoll()
        }
    }

    protected async upload(event: SubmitEvent): Promise<void> {
        event.preventDefault()
        if (
            !(event.currentTarget instanceof HTMLFormElement) ||
            this.uploading() ||
            this.access() !== 'allowed'
        )
            return

        const form = event.currentTarget
        const result = validateUpload(new FormData(form))
        this.feedback.set(null)
        this.uploadError.set(null)
        if (result.error !== undefined) {
            this.uploadError.set(result.error)
            return
        }

        this.uploading.set(true)
        this.percent.set(null)
        try {
            const item = await firstValueFrom(
                this.api
                    .upload(result.data, (percent) => {
                        this.percent.set(percent)
                    })
                    .pipe(takeUntilDestroyed(this.destroyRef)),
            )
            this.addItem(item)
            this.feedback.set('Файл загружен и отправлен на обработку.')
            form.reset()
            this.mediaName.set('')
            this.coverName.set('')
        } catch (error) {
            if (!this.destroyRef.destroyed && !this.checkAccess(error)) {
                this.uploadError.set(errorMessage(error))
            }
        } finally {
            this.uploading.set(false)
        }
    }

    protected async update(item: AdminItem, action: AdminAction): Promise<void> {
        if (this.activeItem() || this.access() !== 'allowed') return

        this.activeItem.set(item.id)
        this.actionError.set(null)
        this.feedback.set(null)
        try {
            const updated = await firstValueFrom(
                this.api.update(item.id, action).pipe(takeUntilDestroyed(this.destroyRef)),
            )
            this.addItem(updated)
            this.feedback.set(updateLabels[action])
        } catch (error) {
            if (!this.destroyRef.destroyed && !this.checkAccess(error)) {
                this.actionError.set(errorMessage(error))
                if (apiErrorCode(error) === 'media_not_ready') void this.refresh()
            }
        } finally {
            this.activeItem.set(null)
        }
    }

    private checkAccess(error: unknown): boolean {
        if (!(error instanceof HttpErrorResponse)) return false
        if (error.status === 401) {
            this.revision++
            this.access.set('unauthenticated')
            this.auth.clear()
            void this.router.navigate(['/login'], { replaceUrl: true })
        } else if (error.status === 403 && apiErrorCode(error) !== 'invalid_origin') {
            this.revision++
            this.access.set('forbidden')
        } else {
            return false
        }

        clearTimeout(this.pollTimer)
        this.items.set([])
        return true
    }

    private addItem(item: AdminItem): void {
        if (this.access() !== 'allowed') return

        this.revision++
        this.items.update((items) =>
            items.some((current) => current.id === item.id)
                ? items.map((current) => (current.id === item.id ? item : current))
                : [item, ...items],
        )
        this.schedulePoll()
    }

    private schedulePoll(): void {
        clearTimeout(this.pollTimer)
        if (
            this.destroyRef.destroyed ||
            this.access() !== 'allowed' ||
            !this.items().some((item) => item.status === 'processing')
        )
            return

        this.pollTimer = setTimeout(() => {
            void this.refresh()
        }, 2500)
    }
}
