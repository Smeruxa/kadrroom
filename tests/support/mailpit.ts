import { expect } from '@playwright/test'
import { z } from 'zod'

const verificationSubject = 'Подтвердите почту для KadrRoom'
const resetSubject = 'Восстановление пароля KadrRoom'
const searchSchema = z.object({
    messages: z.array(
        z.object({
            ID: z.string().min(1),
            Subject: z.string(),
            To: z.array(z.object({ Address: z.string() })),
        }),
    ),
})
const messageSchema = z.object({ Text: z.string() })

export type AccountPurpose = 'verify' | 'reset'

interface Mailbox {
    origin: string
    email: string
}

interface AccountMail {
    id: string
    purpose: AccountPurpose
}

interface AccountLink {
    link: string
    purpose: AccountPurpose
}

export async function existingMessageIds(mailbox: Mailbox): Promise<ReadonlySet<string>> {
    const { messages } = await mailpit(searchURL(mailbox), searchSchema)
    return new Set(messages.map((message) => message.ID))
}

export async function waitForAccountLink(
    mailbox: Mailbox,
    previous: ReadonlySet<string>,
    allowReset: boolean,
): Promise<AccountLink> {
    const search = searchURL(mailbox)
    const received: AccountMail = { id: '', purpose: 'verify' }
    await expect
        .poll(
            async () => {
                const { messages } = await mailpit(search, searchSchema)
                const mail = messages.find(
                    (item) =>
                        !previous.has(item.ID) &&
                        (item.Subject === verificationSubject ||
                            (allowReset && item.Subject === resetSubject)) &&
                        item.To.some((recipient) => recipient.Address === mailbox.email),
                )
                if (!mail) return false

                received.purpose = mail.Subject === resetSubject ? 'reset' : 'verify'
                received.id = mail.ID
                return true
            },
            { message: 'Mailpit receives a new account email', timeout: 25_000 },
        )
        .toBe(true)

    const detail = new URL(`/api/v1/message/${encodeURIComponent(received.id)}`, mailbox.origin)
    const { Text: body } = await mailpit(detail, messageSchema)
    const link = body
        .split(/\r?\n/)
        .map((line) => line.trim())
        .find((line) => /^https?:\/\//.test(line))
    if (!link) throw new Error('The account email has no link')
    return { link, purpose: received.purpose }
}

function searchURL(mailbox: Mailbox): URL {
    const search = new URL('/api/v1/search', mailbox.origin)
    search.searchParams.set('query', `to:${mailbox.email}`)
    return search
}

async function mailpit<Payload>(url: URL, schema: z.ZodType<Payload>): Promise<Payload> {
    const response = await fetch(url, {
        signal: AbortSignal.timeout(5_000),
        redirect: 'error',
    }).catch(() => {
        throw new Error('Mailpit could not be reached')
    })
    if (!response.ok) throw new Error(`Mailpit returned HTTP ${String(response.status)}`)

    const data: unknown = await response.json().catch(() => {
        throw new Error('Mailpit returned invalid JSON')
    })
    const result = schema.safeParse(data)
    if (!result.success) throw new Error('Mailpit returned an unexpected response')
    return result.data
}
