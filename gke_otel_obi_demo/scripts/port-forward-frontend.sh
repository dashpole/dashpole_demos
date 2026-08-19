#!/usr/bin/env bash
# Helper script to port-forward web-frontend ClusterIP service to localhost:8080
set -euo pipefail

echo "Establishing port-forward to web-frontend (ai-agent namespace)..."
echo "Web UI will be accessible at: http://localhost:8080"
kubectl port-forward -n ai-agent svc/web-frontend 8080:8080
