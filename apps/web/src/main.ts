import { provideHttpClient } from '@angular/common/http'
import { bootstrapApplication } from '@angular/platform-browser'
import { CatalogPage } from './catalog-page'

bootstrapApplication(CatalogPage, {
    providers: [provideHttpClient()],
}).catch((error: unknown) => {
    console.error('KadrRoom could not start', error)
})
