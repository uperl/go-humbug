#!/usr/bin/env python3
"""Minimal Humbug client: launches the sidecar, runs the fixture cases, prints a summary.

    make build && python3 examples/python/client.py [path/to/humbug] [spec] [cases]

Uses only the standard library; the protocol is plain NDJSON over a subprocess. See
docs/client-guide.md for what a real client must do (this file is the worked example).
"""
import base64
import json
import subprocess
import sys


class Humbug:
    def __init__(self, exe):
        self.p = subprocess.Popen([exe], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, bufsize=1)
        self.n = 0
        self.limits = {}

    def call(self, type_, params=None):
        self.n += 1
        cmd = {"id": self.n, "type": type_}
        if params is not None:
            cmd["params"] = params
        self.p.stdin.write(json.dumps(cmd) + "\n")
        self.p.stdin.flush()
        line = self.p.stdout.readline()
        if not line:
            # A dead process is a failed transaction; a real client restarts it.
            raise RuntimeError("humbug exited")
        reply = json.loads(line)
        assert reply["id"] == self.n, reply
        return reply

    def initialize(self, spec_path):
        reply = self.call("initialize", {"protocol_version": 1, "spec": {"path": spec_path}})
        if reply["type"] == "result":
            # Optional: another implementation may report other limits, or none.
            self.limits = reply["result"].get("limits", {})
        return reply

    def close(self):
        self.call("shutdown")
        self.p.stdin.close()
        self.p.wait()


def header_entry(name, value):
    """One wire header entry. Text goes as is; bytes that are not UTF-8 are sent base64-encoded."""
    if isinstance(value, bytes):
        try:
            value = value.decode("utf-8")
        except UnicodeDecodeError:
            return [name, base64.b64encode(value).decode(), True]
    return [name, value]


def header_list(headers):
    """Header lines in captured order. Pass a list of (name, value) pairs to keep order and repeats;
    a dict is accepted for convenience but cannot represent a repeated header."""
    pairs = headers.items() if isinstance(headers, dict) else (headers or [])
    return [header_entry(name, value) for name, value in pairs]


def body_base64(body):
    """The body must already be decoded (no gzip, no chunking). Strings are encoded as UTF-8."""
    if body is None:
        return None
    if isinstance(body, str):
        body = body.encode()
    return base64.b64encode(body).decode()


def message(m, response=False):
    out = {"headers": header_list(m.get("headers"))}
    if response:
        out["status"] = m["status"]
    else:
        out["method"] = m["method"]
        out["url"] = "http://example.test" + m["url"]  # absolute URL; scheme and host are not checked
    if m.get("body") is not None:
        out["body_base64"] = body_base64(m["body"])
    return out


def main():
    exe = sys.argv[1] if len(sys.argv) > 1 else "bin/humbug"
    spec = sys.argv[2] if len(sys.argv) > 2 else "testdata/specs/users.yaml"
    cases = sys.argv[3] if len(sys.argv) > 3 else "testdata/cases/cases.json"
    h = Humbug(exe)
    reply = h.initialize(spec)
    print(reply["result"] if reply["type"] == "result" else reply["error"])
    if reply["type"] != "result":
        return 1
    for c in json.load(open(cases)):
        params = {"request": message(c["req"])}
        if c.get("res"):
            params["response"] = message(c["res"], response=True)
        r = h.call("validate", params)
        if r["type"] == "error":
            print(f'{c["id"]:5} ERROR {r["error"]}')
            continue
        res = r["result"]
        codes = [f'{s}:{d["code"]}' for s in ("request", "response") for d in res[s]["diagnostics"]]
        print(f'{c["id"]:5} valid={res["valid"]!s:5} complete={res["complete"]!s:5} {", ".join(codes)}')
    h.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
