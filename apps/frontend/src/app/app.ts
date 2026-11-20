import { Component, inject } from '@angular/core'
import { RouterLink, RouterOutlet } from '@angular/router'
import { AuthApi } from './auth/auth-api'

@Component({
    selector: 'kadrroom-root',
    imports: [RouterLink, RouterOutlet],
    templateUrl: './app.html',
    styleUrl: './app.scss',
})
export class App {
    private readonly auth = inject(AuthApi)
    protected readonly account = this.auth.account

    constructor() {
        void this.auth.refresh().catch(() => undefined)
    }
}
