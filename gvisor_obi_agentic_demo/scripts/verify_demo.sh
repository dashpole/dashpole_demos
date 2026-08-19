#!/usr/bin/env bash
# Copyright 2026 The OpenTelemetry Authors / Google LLC
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${ROOT_DIR}"

echo "================================================================================"
echo "   gVisor <-> OpenTelemetry eBPF Instrumentation (OBI) Verification Suite"
echo "================================================================================"

echo "[1/4] Running Full Test Suite under Race Detector (go test -race)..."
go test -v -race -count=1 ./...

echo -e "\n[2/4] Running Shared Memory Ring Buffer Throughput Benchmark..."
go run ./cmd/ringbuf-bench/main.go -producers 8 -events 2000000

echo -e "\n[3/4] Running End-to-End Multi-Tier Agentic Verification Demo..."
go run ./cmd/demo-runner/main.go

echo -e "\n[4/4] Validating Kubernetes Manifests..."
for manifest in k8s/*.yaml; do
    echo "  - Validating syntax: ${manifest}"
    python3 -c "import yaml; list(yaml.safe_load_all(open('${manifest}')))"
done

echo "================================================================================"
echo "   ALL VERIFICATION GATES PASSED (100% SUCCESS)!"
echo "================================================================================"
