import json
import shipwire

def connect():
    return shipwire.Shipwire(username='dev@hanzo.ai',
                             password='',
                             host='api.shipwire.com')


class Shipwire(object):
    """
    Simple wrapper around Shipwire library.
    """

    def __init__(self, debug=True):
        self.sw    = connect()
        self.debug = debug

    def create_webhook(self, webhook):
        res = self.sw.webhooks.create(json=webhook)
        self.log(webhook, res)
        return res

    def delete_webhook(self, id):
        res = self.sw.webhooks.delete(id=id)
        self.log(id, res)
        return res

    def list_webhooks(self):
        res = self.sw.webhooks.list()
        self.log(None, res)
        return res

    def cancel_order(self, order):
        external_id = 'E' + order.id_
        res = self.sw.order.cancel(id=external_id)
        self.log(external_id, res)
        return res

    def ship_order(self, order, level='GD'):
        sku      = '686696998137'
        quantity = 0

        # Only handle earphones for now
        for item in order.items_:
            if item['productId'] == 'wycZ3j0kFP0JBv':
                quantity += item['quantity']

        req = {
            'orderNo':    order.number,
            'externalId': order.id_,
            'options': {
                'serviceLevelCode': level,
            },
            'shipTo': {
                'email':      order.email,
                'name':       order.shipping_address_name,
                'address1':   order.shipping_address_line1,
                'address2':   order.shipping_address_line2,
                'city':       order.shipping_address_city,
                'state':      order.shipping_address_state,
                'country':    order.shipping_address_country,
                'postalCode': order.shipping_address_postal_code,
            },
            'items': [
                {
                    'sku':      sku,
                    'quantity': quantity,
                }
            ],
        }

        res = self.sw.order.create(json=req)
        self.log(req, res)
        return res

    def log(self, req, res):
        if not self.debug:
            return

        print json.dumps(req, indent=4)
        print '######### BEGIN'
        print res.status, res.message
        print '######### res.json'
        print json.dumps(res.json, indent=4)
        print '######### res.location'
        print res.location
        print '######### res.warnings'
        print res.warnings
        print '######### res.errors'
        print res.errors
        print '######### END'


def write_cache():
    sw = connect()
    r  = sw.orders.list(expand="all")

    with open('_export/shipwire.json', 'w') as f:
        for order in r.all_serial():
            f.write(json.dumps(order['resource']) + '\n')


def read_cache():
    with open('_export/shipwire.json') as f:
        for line in f:
            yield json.loads(line)
