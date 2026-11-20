import { DestroyRef, Directive, DOCUMENT, ElementRef, inject, signal } from '@angular/core'

export type FullscreenMode = 'native' | 'fallback'

@Directive({
    selector: '[kadrroomFullscreen]',
    exportAs: 'kadrroomFullscreen',
    host: {
        '[class.immersive]': "mode() === 'fallback'",
        '(document:fullscreenchange)': 'onFullscreenChange()',
        '(document:keydown.escape)': 'exitFallback($event)',
        '(keydown)': 'moveControlFocus($event)',
    },
})
export class PlaybackFullscreen {
    private readonly document = inject(DOCUMENT)
    private readonly destroyRef = inject(DestroyRef)
    private readonly frame = inject<ElementRef<HTMLElement>>(ElementRef).nativeElement
    readonly mode = signal<FullscreenMode | null>(null)
    readonly placeholderHeight = signal<number | null>(null)

    private pending = false
    private exitButton: HTMLButtonElement | undefined
    private restoreFallback: (() => void) | undefined

    constructor() {
        this.destroyRef.onDestroy(() => {
            this.restoreFallback?.()
        })
    }

    async toggle(button: HTMLButtonElement): Promise<void> {
        if (this.pending) return

        if (this.mode() === 'fallback') {
            this.leave()
            return
        }

        this.pending = true
        try {
            if (this.mode() === 'native') {
                await this.document.exitFullscreen().catch(() => undefined)
                this.onFullscreenChange()
                return
            }

            this.placeholderHeight.set(this.frame.getBoundingClientRect().height)
            const native = await this.enterNative()
            if (this.destroyRef.destroyed) return

            if (!native) this.isolateFallback()
            this.exitButton = button
            this.mode.set(native ? 'native' : 'fallback')
            button.focus({ preventScroll: true })
        } finally {
            this.pending = false
        }
    }

    protected onFullscreenChange(): void {
        if (this.mode() === 'native' && this.document.fullscreenElement !== this.frame) this.leave()
    }

    protected exitFallback(event: Event): void {
        if (this.mode() !== 'fallback') return

        event.preventDefault()
        this.leave()
    }

    protected moveControlFocus(event: KeyboardEvent): void {
        if (event.key !== 'Tab') return

        const elements = Array.from(
            this.frame.querySelectorAll<HTMLElement>('button:enabled, input:enabled'),
        ).filter(
            (element) =>
                this.document.defaultView?.getComputedStyle(element).visibility !== 'hidden',
        )
        const index = elements.findIndex((element) => element === event.target)
        if (index < 0 && event.target !== this.frame) return

        const nextIndex = index + (event.shiftKey ? -1 : 1)
        const next = nextIndex >= 0 ? elements.at(nextIndex) : undefined
        const target = next ?? (this.mode() ? elements.at(event.shiftKey ? -1 : 0) : undefined)
        if (!target) return

        event.preventDefault()
        target.focus()
    }

    private async enterNative(): Promise<boolean> {
        if (!this.document.fullscreenEnabled) return false

        try {
            await this.frame.requestFullscreen()
            return this.document.fullscreenElement === this.frame
        } catch {
            return false
        }
    }

    private isolateFallback(): void {
        const { body, documentElement } = this.document
        const branches = new Map<HTMLElement, HTMLElement>()
        for (
            let branch = this.frame;
            branch !== body && branch.parentElement;
            branch = branch.parentElement
        ) {
            branches.set(branch.parentElement, branch)
        }

        const isolated = new Set<Element>()
        const isolateSiblings = () => {
            for (const [parent, branch] of branches) {
                for (const sibling of parent.children) {
                    if (sibling === branch || sibling.hasAttribute('inert')) continue

                    sibling.setAttribute('inert', '')
                    isolated.add(sibling)
                }
            }
        }
        isolateSiblings()
        const observer = new MutationObserver(isolateSiblings)
        for (const parent of branches.keys()) observer.observe(parent, { childList: true })

        const style = documentElement.style
        const overflow = style.getPropertyValue('overflow')
        const priority = style.getPropertyPriority('overflow')
        style.setProperty('overflow', 'hidden')
        this.restoreFallback = () => {
            observer.disconnect()
            for (const sibling of isolated) sibling.removeAttribute('inert')
            style.setProperty('overflow', overflow, priority)
        }
    }

    private leave(): void {
        this.restoreFallback?.()
        this.restoreFallback = undefined
        this.placeholderHeight.set(null)
        this.mode.set(null)
        this.exitButton?.focus({ preventScroll: true })
        this.exitButton = undefined
    }
}
