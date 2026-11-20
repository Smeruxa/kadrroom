import { ChangeDetectionStrategy, Component, input } from '@angular/core'

export type ButtonVariant = 'primary' | 'secondary'

@Component({
    selector: 'button[krButton]',
    template: '<ng-content />',
    styleUrl: './button.scss',
    changeDetection: ChangeDetectionStrategy.OnPush,
    host: {
        '[attr.data-kr-button]': 'krButton()',
    },
})
export class KrButton {
    readonly krButton = input.required<ButtonVariant>()
}
