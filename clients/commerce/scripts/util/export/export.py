import csv
import sys
import json
from recordtype import recordtype
from .util import snake
from .transform import from_bool, from_json


csv.field_size_limit(sys.maxsize)


class Parser(object):
    """
    Parses CSV row into Python objects with specified attributes,
    transforming columns as necessary.
    """

    def __init__(self, constructor, header, fields):
        self.constructor = constructor
        self.header      = header
        self.fields      = fields

    def get_column(self, row, field):
        idx = self.header[field]
        return row[idx]

    def parse(self, row):
        values = []

        for field, transform in self.fields.items():
            # Skip virtual columns
            if transform is None:
                values.append(None)
                continue

            # Get value for this column
            val = self.get_column(row, field)

            # Apply any necessary transformations
            if transform == str:
                values.append(val)
            elif transform == bool:
                values.append(from_bool(val))
            elif transform == json:
                values.append(from_json(val))
            else:
                values.append(transform(val))

        return self.constructor(*values)


class Export(object):
    """
    Wrapper around on-disk CSV exports which allows them to easily be
    transformed into normal Python objects.
    """
    fields = {}

    def __init__(self, filename):
        class_name    = self.__class__.__name__
        self.filename = filename
        self.header   = self.parse_header()
        self.constructor = recordtype(
            class_name, [
                snake(f) for f in self.fields])

    def parse_header(self):
        """Create map for CSV colum layout based on header."""
        with open(self.filename) as f:
            first_row = next(f).split(',')
            return dict((k.strip(), i) for i, k in enumerate(first_row))

    def get_parser(self):
        """Get parser for this export."""
        return Parser(self.constructor, self.header, self.fields)

    def ignore(self, obj):
        """Ignore specific objects."""
        return False

    def hydrate(self, obj):
        """Hydrate an object."""
        return obj

    def read_csv(self):
        """Lazily read CSV converting rows into records."""
        with open(self.filename) as f:
            next(f)  # Skip header
            parser = self.get_parser()
            for row in csv.reader(f):
                obj = parser.parse(row)
                if not self.ignore(obj):
                    yield self.hydrate(obj)

    def to_list(self):
        """Convert export into list of records."""
        return list(self.read_csv())

    def to_dict(self, key='id_'):
        """Convert export into dict of records using some key."""
        d = {}
        for obj in self.read_csv():
            k = getattr(obj, key)
            d[k] = obj
        return d
