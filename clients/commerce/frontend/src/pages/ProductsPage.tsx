import { CollectionCRUD } from '@hanzogui/admin/crud'

export default function ProductsPage() {
  return (
    <CollectionCRUD
      collection="products"
      apiBase="/v1/commerce"
      columns={['id', 'name', 'sku', 'price', 'currency', 'status']}
    />
  )
}
