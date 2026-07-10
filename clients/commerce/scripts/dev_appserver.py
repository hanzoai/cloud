#!/usr/bin/env python2

import os
import sys
import subprocess
import json

result = subprocess.check_output([
        'gcloud',
        'info',
        "--format=json"
    ])

admin_port = os.environ.get('DEV_APPSERVER_ADMIN_PORT', '8000')
api_port = os.environ.get('DEV_APPSERVER_API_PORT', '8001')
port = os.environ.get('DEV_APPSERVER_PORT', '8080')

GCLOUD_PATH = json.loads(result)['installation']['sdk_root']
SERVER_PATH = os.path.join(GCLOUD_PATH, 'bin/dev_appserver.py')
PLATFORM_PATH = os.path.abspath(os.path.join(os.path.dirname(__file__), '..'))

print(SERVER_PATH)

PORTS = {
    '--admin_port=0': int(admin_port),
    '--api_port=0':   int(api_port),
    '--port=0':       int(port),
}

if __name__ == '__main__':
    # Update dev_appserver ports with randomized port numbers
    for i, argv in enumerate(sys.argv):
        if argv in PORTS:
            sys.argv[i] = argv.replace('0', str(PORTS[argv]))

    # Remove generated app.yaml from arg stack
    sys.argv.pop()

    sys.argv.extend([
        # '--dev_appserver_log_level=info',
        '--enable_task_running=true',
        os.path.join(PLATFORM_PATH, 'config/test'),
        os.path.join(PLATFORM_PATH, 'api/app.development.yaml'),
    ])

    argv = 'python3 {} {}'.format(SERVER_PATH, ' '.join(sys.argv[1:]))
    print(argv)

    sys.exit(os.system(argv))
