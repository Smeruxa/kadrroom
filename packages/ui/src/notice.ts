import { ChangeDetectionStrategy, Component, input } from '@angular/core'

export type NoticeTone = 'info' | 'success' | 'error'

@Component({
    selector: 'p[krNotice]',
    template: '<ng-content />',
    styleUrl: './notice.scss',
    changeDetection: ChangeDetectionStrategy.OnPush,
    host: {
        '[attr.data-kr-notice]': 'krNotice()',
        '[attr.role]': "krNotice() === 'error' ? 'alert' : 'status'",
    },
})
export class KrNotice {
    readonly krNotice = input.required<NoticeTone>()
}
