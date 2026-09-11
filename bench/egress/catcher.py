"""A listener that records what tried to leave, and answers nothing.

Any Go program using net/http honours HTTP_PROXY, HTTPS_PROXY and NO_PROXY, so
a process pointed here reaches this socket instead of the internet. Every
accepted connection is one attempt, written to the log with whatever the client
asked for first — `CONNECT host:443` for TLS, the absolute URL for plain HTTP,
and the raw bytes for anything that is not HTTP at all.

It answers nothing on purpose. A caller that phones home is recorded whether or
not the call would have worked, and a caller that does not phone home is not
kept waiting by a reply it never asked for.
"""
import socket, sys, threading

log = open(sys.argv[2], 'a', buffering=1)
srv = socket.socket()
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(('127.0.0.1', int(sys.argv[1])))
srv.listen(64)
print('ready', flush=True)


def held(conn):
    try:
        conn.settimeout(2)
        first = conn.recv(4096).split(b'\r\n')[0]
        log.write((first.decode('utf-8', 'replace') or '(no request line)') + '\n')
    except Exception as why:
        log.write('(unreadable: %s)\n' % why)
    finally:
        conn.close()


while True:
    c, _ = srv.accept()
    threading.Thread(target=held, args=(c,), daemon=True).start()
