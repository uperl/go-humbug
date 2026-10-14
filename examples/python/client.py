#!/usr/bin/env python3
"""Minimal Humbug client: launches the sidecar, runs the fixture cases, prints a summary.

    make build && python3 examples/python/client.py [path/to/humbug] [spec] [cases]

Uses only the standard library; the protocol is plain NDJSON over a subprocess.
"""
import base64
import json
import subprocess
import sys


class Humbug:
    def __init__(self, exe):
        self.p = subprocess.Popen([exe], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, bufsize=1)
        self.n = 0

    def call(self, type_, params=None):
        self.n += 1
        cmd = {"id": self.n, "type": type_}
        if params is not None:
            cmd["params"] = params
        self.p.stdin.write(json.dumps(cmd) + "\n")
        self.p.stdin.flush()
        reply = json.loads(self.p.stdout.readline())
        assert reply["id"] == self.n, reply
        return reply

    def close(self):
        self.call("shutdown")
        self.p.stdin.close()
        self.p.wait()


def b64(s):
    return None if s is None else base64.b64encode(s.encode()).decode()


def message(m, response=False):
    out = {"headers": [[k, v] for k, v in (m.get("headers") or {}).items()]}
    if response:
        out["status"] = m["status"]
    else:
        out["method"] = m["method"]
        out["url"] = "http://example.test" + m["url"]
    if "body" in m:
        out["body_base64"] = b64(m["body"])
    return out


def main():
    exe = sys.argv[1] if len(sys.argv) > 1 else "bin/humbug"
    spec = sys.argv[2] if len(sys.argv) > 2 else "testdata/specs/users.yaml"
    cases = sys.argv[3] if len(sys.argv) > 3 else "testdata/cases/cases.json"
    h = Humbug(exe)
    print(h.call("initialize", {"protocol_version": 1, "spec": {"path": spec}})["result"])
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


if __name__ == "__main__":
    main()
