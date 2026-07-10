#!/usr/bin/env python
from util.export import Export, json
from util import shipwire


class Order(Export):
    fields = {
        'id_':    str,
        'items_': json,
        'number': str,
        'email':  str,

        'shipping_address_name':        str,
        'shipping_address_country':     str,
        'shipping_address_state':       str,
        'shipping_address_city':        str,
        'shipping_address_postal_code': str,
        'shipping_address_line1':       str,
        'shipping_address_line2':       str,
    }


if __name__ == '__main__':
    sw = shipwire.Shipwire()
    for order in Order('_export/orders.csv').read_csv():
        sw.ship_order(order, level='GD')
