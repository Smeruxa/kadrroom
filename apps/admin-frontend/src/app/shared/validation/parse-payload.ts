import type { ZodType } from 'zod'

export function parsePayload<Result>(schema: ZodType<Result>, payload: unknown): Result {
    return schema.parse(payload)
}
