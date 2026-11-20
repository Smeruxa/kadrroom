import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'

const iconPaths = {
    play: ['M8 5l12 7-12 7z'],
    pause: ['M9 5v14', 'M15 5v14'],
    'back-five': ['M3 4v6h6', 'M3 10a9 9 0 1 1 2.2 8.3', 'M15 8h-4l-.5 4H13a2 2 0 0 1 0 4h-2.5'],
    'forward-five': [
        'M21 4v6h-6',
        'M21 10a9 9 0 1 0-2.2 8.3',
        'M14 8h-4l-.5 4H12a2 2 0 0 1 0 4H9.5',
    ],
    volume: ['M11 5L6 9H3v6h3l5 4z', 'M15 9a4 4 0 0 1 0 6', 'M18 6a8 8 0 0 1 0 12'],
    muted: ['M11 5L6 9H3v6h3l5 4z', 'M16 9l5 6m0-6-5 6'],
    fullscreen: ['M8 4H4v4m12-4h4v4M4 16v4h4m12-4v4h-4'],
    'exit-fullscreen': ['M4 8h4V4m12 4h-4V4M4 16h4v4m12-4h-4v4'],
    copy: [
        'M8 8h9a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2v-9a2 2 0 0 1 2-2z',
        'M6 16H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1',
    ],
    link: [
        'M10 13a4 4 0 0 0 5.7.3l4-4a4 4 0 0 0-5.7-5.7l-2.3 2.3',
        'M14 11a4 4 0 0 0-5.7-.3l-4 4a4 4 0 0 0 5.7 5.7l2.3-2.3',
    ],
    refresh: ['M20 8a8 8 0 0 0-13-3L4 8m0-5v5h5', 'M4 16a8 8 0 0 0 13 3l3-3m0 5v-5h-5'],
    'room-close': ['M5 21h14m-2 0V5a2 2 0 0 0-2-2H9a2 2 0 0 0-2 2v16m6-9h.01'],
    exit: ['M13 4H6a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h7', 'M10 12h11m-4-4 4 4-4 4'],
    check: ['M5 12l4 4L19 6'],
    cancel: ['M6 6l12 12M18 6 6 18'],
    phone: [
        'M21 16.7v2.7a2 2 0 0 1-2.2 2A18 18 0 0 1 2.6 5.2 2 2 0 0 1 4.6 3h2.7a2 2 0 0 1 2 1.7l.5 2.8a2 2 0 0 1-.6 2.1L8 10.8A13.4 13.4 0 0 0 13.2 16l1.2-1.2a2 2 0 0 1 2.1-.6l2.8.5a2 2 0 0 1 1.7 2z',
    ],
    'phone-off': [
        'M3 15v-2.5a2 2 0 0 1 .7-1.5 13.2 13.2 0 0 1 16.6 0 2 2 0 0 1 .7 1.5V15a1 1 0 0 1-1.2 1l-3.6-.8a1 1 0 0 1-.8-1v-1.6a10 10 0 0 0-6.8 0v1.6a1 1 0 0 1-.8 1l-3.6.8A1 1 0 0 1 3 15z',
    ],
    microphone: [
        'M12 3a3 3 0 0 0-3 3v5a3 3 0 0 0 6 0V6a3 3 0 0 0-3-3z',
        'M5 11a7 7 0 0 0 14 0m-7 7v3m-3 0h6',
    ],
    'microphone-off': [
        'M3 3l18 18',
        'M9 9v2a3 3 0 0 0 4.8 2.4M9 6a3 3 0 0 1 6 0v4',
        'M5 11a7 7 0 0 0 11.3 5.5M19 11a7 7 0 0 1-.4 2.3M12 18v3m-3 0h6',
    ],
    camera: [
        'M4 6h10a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2z',
        'M16 10l6-3v10l-6-3',
    ],
    'camera-off': [
        'M3 3l18 18',
        'M7 6h7a2 2 0 0 1 2 2v4m0 4a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V8c0-.7.4-1.4 1-1.7',
        'M16 10l6-3v10l-2.5-1.3',
    ],
} as const

export type KrIconName = keyof typeof iconPaths

@Component({
    selector: 'kr-icon',
    template: `
        <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="1.7"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
            focusable="false"
        >
            @for (path of paths(); track path) {
                <path [attr.d]="path" />
            }
        </svg>
    `,
    styleUrl: './icon.scss',
    changeDetection: ChangeDetectionStrategy.OnPush,
})
export class KrIcon {
    readonly name = input.required<KrIconName>()
    protected readonly paths = computed(() => iconPaths[this.name()])
}
