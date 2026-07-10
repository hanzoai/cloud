import { CollectionCRUD } from '@hanzogui/admin/crud'

export default function CustomersPage() {
  return (
    <CollectionCRUD
      collection="customers"
      apiBase="/v1/commerce"
      columns={['id', 'email', 'name', 'created']}
    />
  )
}
