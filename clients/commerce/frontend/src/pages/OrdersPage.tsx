import { CollectionCRUD } from '@hanzogui/admin/crud'

export default function OrdersPage() {
  return (
    <CollectionCRUD
      collection="orders"
      apiBase="/v1/commerce"
      readOnly
      columns={['id', 'customerId', 'status', 'total', 'currency', 'created']}
      filters={[
        { field: 'status', type: 'select', options: ['pending', 'paid', 'fulfilled', 'shipped', 'delivered', 'cancelled', 'refunded'] },
      ]}
    />
  )
}
