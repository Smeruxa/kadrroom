import { Component, computed, input, output, signal } from '@angular/core'
import type { CatalogItem } from '@kadrroom/contracts'
import { formatDuration } from '../../shared/formatting/format-duration'

const catalogPosters = new Map([
    ['last-tram', 'media/last-tram.webp'],
    ['lighthouse-window', 'media/lighthouse-window.webp'],
    ['blue-light', 'media/blue-light.webp'],
])

@Component({
    selector: 'kadrroom-catalog-card',
    templateUrl: './catalog-card.html',
    styleUrl: './catalog-card.scss',
})
export class CatalogCard {
    readonly item = input.required<CatalogItem>()
    readonly featured = input(false)
    readonly creating = input(false)
    readonly create = output<CatalogItem>()

    private readonly failedPoster = signal<string | undefined>(undefined)
    protected readonly duration = computed(() => formatDuration(this.item().durationSeconds))
    protected readonly poster = computed(() => {
        const item = this.item()
        const poster = item.coverUrl ?? catalogPosters.get(item.id)
        return poster === this.failedPoster() ? undefined : poster
    })

    protected hidePoster(poster: string): void {
        this.failedPoster.set(poster)
    }
}
