import { provideHttpClient, withXhr } from '@angular/common/http'
import { bootstrapApplication } from '@angular/platform-browser'
import { provideRouter } from '@angular/router'
import { AdminApp } from './app/app'
import { routes } from './app/app.routes'

bootstrapApplication(AdminApp, {
    providers: [provideHttpClient(withXhr()), provideRouter(routes)],
}).catch((error: unknown) => {
    console.error('KadrRoom Admin could not start', error)
})
