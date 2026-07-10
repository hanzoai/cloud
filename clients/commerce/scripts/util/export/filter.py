def open(order):
    return order.status == 'open' and order.payment_status == 'paid'


def cancelled(order):
    return order.status == 'cancelled' or order.payment_status == 'refunded'


def locked(order):
    return order.status == 'locked'


def disputed(order):
    return order.payment_status == 'disputed'


def invalid(order):
    return not open(order) and not cancelled(order) and not disputed(order)


def domestic(order):
    return order.shipping_address_country == 'US'


def international(order):
    return not domestic(order)


def batch1(order):
    return order.batch == 1


def f2k(order):
    return order.batch == 'f2k'


def processed(order):
    return order.s_status and order.s_status != 'cancelled'


def from2016(order):
    return order.created_at.year == 2016


def contacted_us(order):
    return order.r_user


def partial_refund(order):
    return order.refunded != 0
