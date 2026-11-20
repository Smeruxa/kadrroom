import { Directive, ElementRef, effect, inject, input } from '@angular/core'
import type { Track } from 'livekit-client'

@Directive({ selector: 'audio[kadrroomCallTrack], video[kadrroomCallTrack]' })
export class CallTrack {
    readonly track = input.required<Track>({ alias: 'kadrroomCallTrack' })

    private readonly element = inject<ElementRef<HTMLMediaElement>>(ElementRef)

    constructor() {
        effect((cleanup) => {
            const track = this.track()
            const element = this.element.nativeElement
            track.attach(element)
            cleanup(() => {
                track.detach(element)
            })
        })
    }
}
