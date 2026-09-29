#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Add one WireGuard UDP adapter and Zenoh bridge to a Liqo Gateway template.

Read the stock WgGateway*Template JSON from stdin and write a new template to
stdout. A distinct service key is required for every cluster pair.
"""

import argparse
import json
import re
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--role", choices=("client", "server"), required=True)
    parser.add_argument("--name", required=True)
    parser.add_argument("--service-key", required=True)
    parser.add_argument("--adapter-image", required=True)
    parser.add_argument("--bridge-image", required=True)
    parser.add_argument("--secret-name", default="zenoh-config")
    parser.add_argument("--namespace", default="liqo")
    args = parser.parse_args()

    if not re.fullmatch(r"[A-Za-z0-9_.-]+", args.service_key):
        parser.error("--service-key must contain only letters, digits, '_', '-', or '.'")

    template = json.load(sys.stdin)
    expected_kind = "WgGatewayClientTemplate" if args.role == "client" else "WgGatewayServerTemplate"
    if template.get("kind") != expected_kind:
        parser.error(f"expected {expected_kind}, got {template.get('kind')!r}")

    template["metadata"] = {"name": args.name, "namespace": args.namespace}
    template.pop("status", None)
    pod = template["spec"]["template"]["spec"]["deployment"]["spec"]["template"]["spec"]
    containers = pod["containers"]
    if [container["name"] for container in containers] != ["gateway", "wireguard", "geneve"]:
        parser.error("stock Gateway template must contain gateway, wireguard, geneve in that order")

    if args.role == "client":
        wireguard = containers[1]
        replaced_address = replaced_port = False
        for index, value in enumerate(wireguard["args"]):
            if value.startswith("--endpoint-address="):
                wireguard["args"][index] = "--endpoint-address=127.0.0.1"
                replaced_address = True
            elif value.startswith("--endpoint-port="):
                wireguard["args"][index] = "--endpoint-port=51820"
                replaced_port = True
        if not (replaced_address and replaced_port):
            parser.error("client WireGuard endpoint arguments were not found")
        adapter_args = [
            "--mode=dial",
            "--udp-listen=127.0.0.1:51820",
            "--tcp-address=127.0.0.1:55001",
            "--reconnect-delay=250ms",
        ]
        bridge_args = ["--listen", f"{args.service_key}/127.0.0.1:55001,proto=raw"]
    else:
        if not any(value.startswith("--listen-port=") for value in containers[1]["args"]):
            parser.error("server WireGuard listen-port argument was not found")
        adapter_args = [
            "--mode=listen",
            "--udp-listen=127.0.0.1:55002",
            "--udp-target=127.0.0.1:{{ .Spec.Endpoint.Port }}",
            "--tcp-address=127.0.0.1:55002",
        ]
        bridge_args = ["--backend", f"{args.service_key}/127.0.0.1:55002"]

    containers.extend(
        [
            {
                "name": "udp-adapter",
                "image": args.adapter_image,
                "imagePullPolicy": "Never",
                "args": adapter_args,
                "securityContext": {
                    "allowPrivilegeEscalation": False,
                    "readOnlyRootFilesystem": True,
                    "runAsNonRoot": True,
                    "runAsUser": 65532,
                    "capabilities": {"drop": ["ALL"]},
                },
                "resources": {"requests": {"cpu": "50m", "memory": "32Mi"}},
            },
            {
                "name": "zenoh-bridge",
                "image": args.bridge_image,
                "imagePullPolicy": "IfNotPresent",
                "args": bridge_args
                + ["--zenoh-config", "/etc/zenoh/config.json5", "--reliability", "stream"],
                "volumeMounts": [
                    {"name": "zenoh-config", "mountPath": "/etc/zenoh", "readOnly": True}
                ],
                "securityContext": {
                    "allowPrivilegeEscalation": False,
                    "readOnlyRootFilesystem": True,
                    "capabilities": {"drop": ["ALL"]},
                },
                "resources": {"requests": {"cpu": "50m", "memory": "64Mi"}},
            },
        ]
    )
    pod["volumes"].append(
        {"name": "zenoh-config", "secret": {"secretName": args.secret_name, "defaultMode": 288}}
    )
    json.dump(template, sys.stdout, indent=2, ensure_ascii=False)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
