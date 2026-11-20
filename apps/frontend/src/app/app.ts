import { Component, inject } from '@angular/core'
import { RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router'
import type { IsActiveMatchOptions } from '@angular/router'
import { AuthApi } from './auth/auth-api'

@Component({
    selector: 'kadrroom-root',
    imports: [RouterLink, RouterLinkActive, RouterOutlet],
    templateUrl: './app.html',
    styleUrl: './app.scss',
})
export class App {
    private readonly auth = inject(AuthApi)
    protected readonly account = this.auth.account
    protected readonly navActiveOptions: IsActiveMatchOptions = {
        paths: 'exact',
        queryParams: 'ignored',
        fragment: 'ignored',
        matrixParams: 'ignored',
    }

    constructor() {
        void this.auth.refresh().catch(() => undefined)
    }
}
