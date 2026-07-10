import requests
import json
import time
from requests.auth import HTTPBasicAuth


API_ENDPOINT = 'https://stoned.reamaze.com/api/v1/'
API_USER     = 'dev@hanzo.ai'
API_TOKEN    = ''


def get(url, page=None):
    auth = HTTPBasicAuth(API_USER, API_TOKEN)
    headers = {'Accept': 'application/json'}

    if page:
        url = url + '?page=' + str(page)

    res = requests.get(url=API_ENDPOINT + url, auth=auth, headers=headers)

    # Asked to retry
    if res.status_code == 429:
        time.sleep(3)
        return get(url)

    return res.json()


def get_contacts(page=None):
    return get('contacts', page=page)


def write_cache():
    first    = get_contacts()
    contacts = first['contacts']
    pages    = first['page_count']

    for page in range(2, pages+1):
        contacts += get_contacts(page=page)['contacts']

    with open('_export/reamaze.json', 'w') as f:
        for contact in contacts:
            f.write(json.dumps(contact) + '\n')


def read_cache():
    with open('_export/reamaze.json') as f:
        for line in f:
            yield json.loads(line)
