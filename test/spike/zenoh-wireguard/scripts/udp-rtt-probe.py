#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
import argparse
import json
import socket
import statistics
import time
from pathlib import Path


def percentile(sorted_values, percent):
    if not sorted_values:
        return None
    index = (len(sorted_values) - 1) * percent / 100
    low = int(index)
    high = min(low + 1, len(sorted_values) - 1)
    return sorted_values[low] + (sorted_values[high] - sorted_values[low]) * (index - low)


def probe(name, host, port, count, interval_ms, timeout_ms):
    rtts = []
    failures = []
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.connect((host, port))
    sock.settimeout(timeout_ms / 1000)
    try:
        for sequence in range(count):
            payload = (f"liqo-rtt-{name}-{sequence:06d}").encode().ljust(64, b"x")
            started = time.perf_counter_ns()
            try:
                sock.send(payload)
                while True:
                    reply = sock.recv(2048)
                    if reply == payload:
                        rtts.append((time.perf_counter_ns() - started) / 1_000_000)
                        break
                    failures.append({"sequence": sequence, "reason": "unexpected reply"})
            except socket.timeout:
                failures.append({"sequence": sequence, "reason": "timeout"})
            time.sleep(interval_ms / 1000)
    finally:
        sock.close()
    ordered = sorted(rtts)
    return {
        "name": name, "target": f"{host}:{port}", "sent": count,
        "received": len(rtts), "lost": count - len(rtts),
        "min_ms": min(ordered) if ordered else None,
        "mean_ms": statistics.mean(ordered) if ordered else None,
        "p50_ms": percentile(ordered, 50),
        "p95_ms": percentile(ordered, 95),
        "p99_ms": percentile(ordered, 99),
        "max_ms": max(ordered) if ordered else None,
        "rtts_ms": rtts, "failures": failures,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--phase", required=True)
    parser.add_argument("--target", action="append", required=True, help="name=ip:port")
    parser.add_argument("--count", type=int, default=300)
    parser.add_argument("--interval-ms", type=float, default=20)
    parser.add_argument("--timeout-ms", type=float, default=1000)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    if args.count <= 0 or args.interval_ms < 0 or args.timeout_ms <= 0:
        parser.error("count and timeout must be positive; interval cannot be negative")
    result = {"phase": args.phase, "started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
              "count": args.count, "interval_ms": args.interval_ms, "timeout_ms": args.timeout_ms,
              "targets": []}
    for target in args.target:
        name, address = target.split("=", 1)
        host, port = address.rsplit(":", 1)
        item = probe(name, host, int(port), args.count, args.interval_ms, args.timeout_ms)
        result["targets"].append(item)
        print(json.dumps({key: value for key, value in item.items() if key not in ("rtts_ms", "failures")}), flush=True)
    Path(args.output).write_text(json.dumps(result, indent=2) + "\n")


if __name__ == "__main__":
    main()
