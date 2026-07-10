import json


def from_bool(b):
    """Deserialize boolean column into Python bool."""
    if b == 'True':
        return True
    elif b == 'False':
        return False
    else:
        raise Exception('Invalid boolean: {0}'.format(b))


def from_json(obj):
    """Deserialize JSON object."""
    try:
        return json.loads(obj)
    except Exception:
        return {}
