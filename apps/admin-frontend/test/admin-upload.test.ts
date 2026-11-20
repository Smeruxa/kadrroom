import { strictEqual } from 'node:assert/strict'
import { test } from 'node:test'
import { validateUpload } from '../src/app/admin/upload-form.ts'

function uploadForm(): FormData {
    const data = new FormData()
    data.set('title', ' Фильм ')
    data.set('mediaType', 'video')
    data.set('description', '')
    data.set('file', new File(['movie'], 'film.mp4', { type: 'video/mp4' }))
    return data
}

await test('normalizes an upload while omitting the empty optional cover', () => {
    const data = uploadForm()
    data.set('cover', new File([], ''))

    const result = validateUpload(data)

    strictEqual(result.data?.get('title'), 'Фильм')
    strictEqual(result.data.has('cover'), false)
})

await test('rejects invalid titles, media types, and missing media', () => {
    const blankTitle = uploadForm()
    blankTitle.set('title', ' ')
    const wrongType = uploadForm()
    wrongType.set('mediaType', 'image')
    const emptyFile = uploadForm()
    emptyFile.set('file', new File([], 'empty.mp4'))

    strictEqual(validateUpload(blankTitle).error, 'Введите название длиной до 160 символов.')
    strictEqual(validateUpload(wrongType).error, 'Выберите тип медиа.')
    strictEqual(validateUpload(emptyFile).error, 'Выберите непустой медиафайл.')
})

await test('accepts JPEG and rejects empty, oversized or unsupported covers', () => {
    const jpeg = uploadForm()
    jpeg.set('cover', new File(['cover'], 'cover.jpg', { type: 'image/jpeg' }))
    const empty = uploadForm()
    empty.set('cover', new File([], 'empty.png', { type: 'image/png' }))
    const oversized = uploadForm()
    oversized.set(
        'cover',
        new File([new Uint8Array(5 * 1024 * 1024 + 1)], 'large.png', { type: 'image/png' }),
    )
    const unsupported = uploadForm()
    unsupported.set('cover', new File(['cover'], 'cover.webp', { type: 'image/webp' }))

    strictEqual(validateUpload(jpeg).data?.has('cover'), true)
    strictEqual(validateUpload(empty).error, 'Файл обложки должен быть непустым и не больше 5 МиБ.')
    strictEqual(
        validateUpload(oversized).error,
        'Файл обложки должен быть непустым и не больше 5 МиБ.',
    )
    strictEqual(validateUpload(unsupported).error, 'Выберите обложку в формате JPEG или PNG.')
})
