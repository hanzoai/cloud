#!/usr/bin/env python
from datetime import datetime

from util.export import Export, json, latest_csv, to_csv
from util.shipwire import Shipwire


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
    sw = Shipwire()
    for order in Order('orders.csv').read_csv():
        sw.cancel_order(order)
