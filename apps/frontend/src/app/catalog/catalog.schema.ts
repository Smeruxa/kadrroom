import type { CatalogResponse } from '@kadrroom/contracts'
import { z } from 'zod'

export const catalogSchema = z.object({
    items: z.array(
        z.object({
            id: z.string(),
            title: z.string(),
            description: z.string(),
            durationSeconds: z.number().nonnegative(),
            available: z.boolean(),
            mediaType: z.enum(['video', 'audio']).optional(),
            coverUrl: z
                .string()
                .regex(/^\/v1\/covers\/[a-zA-Z0-9_-]+$/)
                .optional(),
        }),
    ),
}) satisfies z.ZodType<CatalogResponse>
