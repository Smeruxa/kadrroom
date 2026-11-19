import { httpResource } from '@angular/common/http'
import { ChangeDetectionStrategy, Component } from '@angular/core'
import { formatDuration, parseCatalog } from './catalog'

@Component({
    selector: 'kadrroom-root',
    templateUrl: './catalog-page.html',
    changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CatalogPage {
    protected readonly catalog = httpResource(() => '/v1/catalog', {
        parse: parseCatalog,
        defaultValue: [],
    })
    protected readonly formatDuration = formatDuration
}
