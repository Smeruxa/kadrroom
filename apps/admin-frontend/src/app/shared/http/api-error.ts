import { HttpErrorResponse } from '@angular/common/http'
import { z } from 'zod'

const apiErrorSchema = z.object({ code: z.string().optional(), error: z.string().optional() })

export function apiErrorCode(error: unknown): string | undefined {
    if (!(error instanceof HttpErrorResponse)) return undefined

    const result = apiErrorSchema.safeParse(error.error)
    return result.success ? (result.data.code ?? result.data.error) : undefined
}
