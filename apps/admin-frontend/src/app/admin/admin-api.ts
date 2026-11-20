import { HttpClient, HttpEventType, HttpResponse } from '@angular/common/http'
import { Injectable, inject } from '@angular/core'
import { filter, map, tap } from 'rxjs'
import { z } from 'zod'
import { parsePayload } from '../shared/validation/parse-payload'

const adminItemSchema = z.object({
    id: z.string().min(1),
    title: z.string().min(1),
    description: z.string(),
    mediaType: z.enum(['video', 'audio']),
    status: z.enum(['processing', 'ready', 'failed', 'published']),
    durationSeconds: z.number().nonnegative(),
    coverUrl: z.string().min(1).optional(),
})
const adminCatalogSchema = z.object({ items: z.array(adminItemSchema) })

export type AdminItem = z.infer<typeof adminItemSchema>
export type AdminAction = 'publish' | 'unpublish' | 'retry'

const catalogURL = '/v1/admin/catalog'
const requestOptions = { timeout: 6000 }

@Injectable()
export class AdminApi {
    private readonly http = inject(HttpClient)

    list() {
        return this.http
            .get<unknown>(catalogURL, requestOptions)
            .pipe(map((payload) => parsePayload(adminCatalogSchema, payload).items))
    }

    upload(data: FormData, onProgress: (percent: number | null) => void) {
        return this.http
            .post<unknown>(catalogURL, data, {
                observe: 'events',
                reportUploadProgress: true,
                timeout: 600_000,
            })
            .pipe(
                tap((event) => {
                    if (event.type === HttpEventType.UploadProgress) {
                        onProgress(
                            event.total
                                ? Math.min(100, Math.floor((event.loaded / event.total) * 100))
                                : null,
                        )
                    }
                }),
                filter((event): event is HttpResponse<unknown> => event instanceof HttpResponse),
                map((event) => parsePayload(adminItemSchema, event.body)),
            )
    }

    update(id: string, action: AdminAction) {
        return this.http
            .post<unknown>(`${catalogURL}/${encodeURIComponent(id)}/${action}`, {}, requestOptions)
            .pipe(map((payload) => parsePayload(adminItemSchema, payload)))
    }
}
