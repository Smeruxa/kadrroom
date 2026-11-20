import { HttpClient, HttpErrorResponse } from '@angular/common/http'
import { Component, DestroyRef, computed, inject, signal } from '@angular/core'
import type { OnInit } from '@angular/core'
import { takeUntilDestroyed } from '@angular/core/rxjs-interop'
import { ActivatedRoute, Router } from '@angular/router'
import type { CatalogItem, MediaType } from '@kadrroom/contracts'
import { KrButton, KrNotice } from '@kadrroom/ui'
import { map } from 'rxjs'
import { RoomApi } from '../room/room-api'
import { parsePayload } from '../shared/validation/parse-payload'
import { CatalogCard } from './card/catalog-card'
import { catalogSchema } from './catalog.schema'

interface LoadingCatalog {
    status: 'loading'
}

interface FailedCatalog {
    status: 'error'
}

interface LoadedCatalog {
    status: 'loaded'
    items: CatalogItem[]
}

type CatalogState = LoadingCatalog | FailedCatalog | LoadedCatalog
type CatalogFilter = 'all' | MediaType

@Component({
    selector: 'kadrroom-catalog-page',
    imports: [CatalogCard, KrButton, KrNotice],
    templateUrl: './catalog-page.html',
    styleUrl: './catalog-page.scss',
})
export class CatalogPage implements OnInit {
    private readonly http = inject(HttpClient)
    private readonly destroyRef = inject(DestroyRef)
    private readonly roomApi = inject(RoomApi)
    private readonly router = inject(Router)
    private readonly route = inject(ActivatedRoute)
    protected readonly state = signal<CatalogState>({ status: 'loading' })
    protected readonly filter = signal<CatalogFilter>('all')
    protected readonly visibleItems = computed(() => {
        const catalog = this.state()
        if (catalog.status !== 'loaded') return []
        const filter = this.filter()
        if (filter !== 'all') {
            return catalog.items.filter((item) => (item.mediaType ?? 'video') === filter)
        }

        const featured = catalog.items.find((item) => item.available)
        return featured && featured !== catalog.items[0]
            ? [featured, ...catalog.items.filter((item) => item !== featured)]
            : catalog.items
    })
    protected readonly creating = signal<string | null>(null)
    protected readonly roomError = signal<string | null>(null)
    protected readonly selectedMedia = computed(() => {
        const id = this.route.snapshot.queryParamMap.get('media')
        const catalog = this.state()
        return catalog.status === 'loaded'
            ? catalog.items.find((item) => item.id === id)
            : undefined
    })

    ngOnInit(): void {
        this.loadCatalog()
    }

    protected async createRoom(item: CatalogItem): Promise<void> {
        if (!item.available || this.creating()) return

        this.creating.set(item.id)
        this.roomError.set(null)
        try {
            const room = await this.roomApi.create(item.id)
            await this.router.navigate(['/rooms', room.roomId], { info: room.invite })
        } catch (error) {
            if (error instanceof HttpErrorResponse && error.status === 401) {
                await this.router.navigate(['/login'], { queryParams: { media: item.id } })
            } else {
                this.roomError.set('Не удалось создать комнату. Попробуйте ещё раз.')
            }
        } finally {
            this.creating.set(null)
        }
    }

    protected loadCatalog(): void {
        this.state.set({ status: 'loading' })
        this.http
            .get<unknown>('/v1/catalog', { timeout: 5000 })
            .pipe(
                map((payload) => parsePayload(catalogSchema, payload)),
                takeUntilDestroyed(this.destroyRef),
            )
            .subscribe({
                next: ({ items }) => {
                    this.state.set({ status: 'loaded', items })
                },
                error: () => {
                    this.state.set({ status: 'error' })
                },
            })
    }
}
