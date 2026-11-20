const mediaLimit = 128 * 1024 * 1024
const coverLimit = 5 * 1024 * 1024
const coverTypes = new Set(['image/jpeg', 'image/png'])

interface ValidUpload {
    data: FormData
    error?: undefined
}

interface InvalidUpload {
    data?: undefined
    error: string
}

type UploadResult = ValidUpload | InvalidUpload

export function validateUpload(data: FormData): UploadResult {
    const title = data.get('title')
    const mediaType = data.get('mediaType')
    const media = data.get('file')
    const cover = data.get('cover')

    if (typeof title !== 'string' || !title.trim() || title.trim().length > 160) {
        return { error: 'Введите название длиной до 160 символов.' }
    }
    if (mediaType !== 'video' && mediaType !== 'audio') {
        return { error: 'Выберите тип медиа.' }
    }
    if (!(media instanceof File) || media.size === 0) {
        return { error: 'Выберите непустой медиафайл.' }
    }
    if (media.size > mediaLimit) {
        return { error: 'Медиафайл должен быть не больше 128 МиБ.' }
    }
    if (cover instanceof File && cover.name) {
        if (cover.size === 0 || cover.size > coverLimit) {
            return { error: 'Файл обложки должен быть непустым и не больше 5 МиБ.' }
        }
        const supportedType = cover.type
            ? coverTypes.has(cover.type)
            : /\.(?:jpe?g|png)$/i.test(cover.name)
        if (!supportedType) {
            return { error: 'Выберите обложку в формате JPEG или PNG.' }
        }
    } else {
        data.delete('cover')
    }

    data.set('title', title.trim())
    return { data }
}
