#!/usr/bin/env python
from util import shipwire


topics = [
    # orders
    'v1.order.created',
    'v1.order.updated',
    'v1.order.canceled',
    'v1.order.completed',
    'v1.order.hold.added',
    'v1.order.hold.cleared',

    # tracking info
    'v1.tracking.created',
    'v1.tracking.updated',
    'v1.tracking.delivered',

    # stock
    'v1.stock.transition',
    'v1.stock.transition.good',
    'v1.alert.low-stock',
    'v1.alert',

    # returns
    'v1.return.created',
    'v1.return.updated',
    'v1.return.canceled',
    'v1.return.completed',
    'v1.return.hold.added',
    'v1.return.hold.cleared',
    'v1.return.tracking.created',
    'v1.return.tracking.updated',
    'v1.return.tracking.delivered',
]


if __name__ == '__main__':
    sw = shipwire.Shipwire()
    for wh in sw.list_webhooks().all():
        sw.delete_webhook(wh['resource']['id'])

    url = 'https://api.hanzo.ai/shipwire/webhook/stoned'
    for topic in topics:
        sw.create_webhook({'topic': topic, 'url': url})
