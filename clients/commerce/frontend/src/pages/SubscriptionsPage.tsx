import { CollectionCRUD } from '@hanzogui/admin/crud'

export default function SubscriptionsPage() {
  return (
    <CollectionCRUD
      collection="subscriptions"
      apiBase="/v1/commerce"
      columns={['id', 'customerId', 'status', 'currentPeriodEnd', 'plan']}
      filters={[
        { field: 'status', type: 'select', options: ['active', 'trialing', 'past_due', 'cancelled', 'unpaid'] },
      ]}
    />
  )
}
