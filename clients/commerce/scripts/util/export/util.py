import csv
import glob
import json
import os
import re
import sys
from datetime import datetime

reload(sys)
sys.setdefaultencoding('utf8')


def snake(name):
    """Converts a Go-style name into the Python equivalent."""
    s1 = re.sub('(.)([A-Z][a-z]+)', r'\1_\2', name)
    s2 = re.sub('([a-z0-9])([A-Z])', r'\1_\2', s1).lower()
    s3 = s2.replace('.', '')
    return s3


def guess_fields(obj):
    """Guess fields expected to export based on public attributes of a Python object."""
    fields = []
    for attr in dir(obj):
        if attr.startswith('_'):
            continue

        if not callable(getattr(obj, attr)):
            fields.append(attr)

    return fields


def to_json(obj):
    """Serialize object to JSON, but do not quote strings."""
    if isinstance(obj, basestring):
        return obj

    def serializer(obj):
        if isinstance(obj, datetime):
            return obj.isoformat()
        raise TypeError("Type not serializable")

    s = json.dumps(obj, default=serializer)

    return re.sub(r'^"|"$', '', s)


def to_csv(rows, filename, fields=()):
    """Write list of entities into CSV."""
    if len(rows) == 0:
        print 'No data to write'
        return

    rows  = iter(rows)
    first = next(rows)

    if not fields:
        fields = guess_fields(first)

    with open(filename, 'w') as f:
        writer = csv.writer(f)
        writer.writerow(fields)

        def write(obj):
            values = (getattr(obj, x) for x in fields)
            serialized = [to_json(x) for x in values]
            writer.writerow(serialized)

        write(first)
        for row in rows:
            write(row)


def latest_csv(kind):
    """Find latest export CSV for a given kind."""
    files = filter(os.path.isfile, glob.glob('_export/*crowdstart-us*.csv'))
    files.sort(key=lambda x: os.path.getmtime(x), reverse=True)
    for fn in files:
        if fn.split('-')[1] == kind.lower():
            return fn
    return None
