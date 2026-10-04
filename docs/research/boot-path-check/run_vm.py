#!/usr/bin/env python3
"""Measure boot time of a microVM by timestamping serial console output.

Spawns the VMM, reads stdout (guest serial) line by line, records wall time
relative to process launch for regex markers, and can send input to the
guest via the VMM's stdin.

Rules: --rule '<regex>|<action>' where action is:
  record:NAME      record time of first match
  send:TEXT        on first match, write TEXT + newline to VMM stdin
"""
import argparse
import json
import os
import re
import select
import socket
import subprocess
import threading
import time


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--name", required=True)
    ap.add_argument("--timeout", type=float, default=45.0)
    ap.add_argument("--log-dir", required=True)
    ap.add_argument("--rule", action="append", default=[])
    ap.add_argument("--done-on", action="append", default=[],
                    help="marker name; exit early once all have fired")
    ap.add_argument("--connect-unix", help="connect to a unix socket for "
                    "serial I/O instead of the process stdio")
    ap.add_argument("--unix-wait", type=float, default=15.0)
    ap.add_argument("cmd", nargs=argparse.REMAINDER)
    a = ap.parse_args()

    rules = []
    for r in a.rule:
        pat, _, act = r.partition("|")
        rules.append((re.compile(pat.encode()), act))

    cmd = a.cmd
    if cmd and cmd[0] == "--":
        cmd = cmd[1:]

    os.makedirs(a.log_dir, exist_ok=True)
    logpath = os.path.join(a.log_dir, a.name + ".serial.log")
    errpath = os.path.join(a.log_dir, a.name + ".vmm-stderr.log")

    t0 = time.monotonic()
    markers = {}
    fired = set()
    lock = threading.Lock()

    sock = None
    writer = [None]  # set after Popen

    def send_text(val):
        try:
            if writer[0] is not None:
                writer[0](val)
        except Exception:
            pass

    def process_line(line, t):
        for key in list(rules):
            pat, act = key
            if key in fired:
                continue
            if pat.search(line):
                fired.add(key)
                kind, _, val = act.partition(":")
                if kind == "record":
                    with lock:
                        markers.setdefault(val, round(t, 4))
                elif kind == "send":
                    send_text(val)

    with open(logpath, "wb") as log, open(errpath, "wb") as errf:
        p = subprocess.Popen(cmd, stdin=subprocess.PIPE,
                             stdout=subprocess.PIPE, stderr=errf)

        def _pipe_write(val):
            p.stdin.write((val + "\n").encode())
            p.stdin.flush()

        writer[0] = _pipe_write
        if a.connect_unix:
            deadline_connect = time.monotonic() + a.unix_wait
            while time.monotonic() < deadline_connect:
                if os.path.exists(a.connect_unix):
                    try:
                        sock = socket.socket(socket.AF_UNIX,
                                             socket.SOCK_STREAM)
                        sock.connect(a.connect_unix)
                        break
                    except OSError:
                        sock = None
                time.sleep(0.02)
            if sock is None:
                p.terminate()
                p.wait()
                print(json.dumps({"name": a.name,
                                  "error": "unix connect failed",
                                  "path": a.connect_unix}))
                return
            markers["socket_connected"] = round(time.monotonic() - t0, 4)

            def _sock_write(val):
                sock.sendall((val + "\n").encode())

            writer[0] = _sock_write
            fd = sock.fileno()
        else:
            fd = p.stdout.fileno()

        def reader():
            buf = b""
            while True:
                r, _, _ = select.select([fd], [], [], 0.2)
                if r:
                    try:
                        data = os.read(fd, 4096)
                    except OSError:
                        break
                    if not data:
                        break
                    t = time.monotonic() - t0
                    log.write(data)
                    log.flush()
                    buf += data
                    while b"\n" in buf:
                        line, _, buf = buf.partition(b"\n")
                        process_line(line, t)
                    if buf:
                        # partial line (e.g. a prompt with no newline); match
                        # but keep it in the buffer in case more arrives
                        process_line(buf, t)
                elif p.poll() is not None:
                    # drain what's left
                    try:
                        data = os.read(fd, 4096)
                    except OSError:
                        break
                    if not data:
                        break
                    t = time.monotonic() - t0
                    log.write(data)
                    log.flush()
                    buf += data
                    while b"\n" in buf:
                        line, _, buf = buf.partition(b"\n")
                        process_line(line, t)
                    if buf:
                        process_line(buf, t)

        th = threading.Thread(target=reader, daemon=True)
        th.start()

        deadline = t0 + a.timeout
        while time.monotonic() < deadline:
            if p.poll() is not None:
                break
            if a.done_on:
                with lock:
                    have = set(markers.keys())
                if all(name in have for name in a.done_on):
                    break
            time.sleep(0.02)

        rc = p.poll()
        if rc is None:
            p.terminate()
            try:
                p.wait(timeout=2)
            except subprocess.TimeoutExpired:
                p.kill()
                p.wait()
            rc = p.returncode
        th.join(timeout=1.0)

    out = {
        "name": a.name,
        "markers": markers,
        "wall_total": round(time.monotonic() - t0, 4),
        "exit_code": rc,
        "serial_log": logpath,
    }
    print(json.dumps(out))


if __name__ == "__main__":
    main()
