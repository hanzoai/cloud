import { CollectionCRUD } from '@hanzogui/admin/crud'

export default function InvoicesPage() {
  return (
    <CollectionCRUD
      collection="invoices"
      apiBase="/v1/commerce"
      readOnly
      columns={['id', 'customerId', 'status', 'total', 'currency', 'dueDate']}
      filters={[
        { field: 'status', type: 'select', options: ['draft', 'open', 'paid', 'void', 'uncollectible'] },
      ]}
    />
  )
}
