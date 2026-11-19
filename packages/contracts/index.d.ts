export interface CatalogItem {
    id: string
    title: string
    description: string
    durationSeconds: number
    available: boolean
}

export interface CatalogResponse {
    items: CatalogItem[]
}
