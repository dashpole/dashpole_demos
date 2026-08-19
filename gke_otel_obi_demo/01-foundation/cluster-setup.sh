#!/usr/bin/env bash
# ==============================================================================
# Milestone 1: Provision GKE Standard Cluster with Managed OpenTelemetry
# ==============================================================================

set -euo pipefail

PROJECT_ID="${PROJECT_ID:-dashpole-dev}"
ZONE="${ZONE:-us-central1-a}"
CLUSTER_NAME="${CLUSTER_NAME:-obi-demo-cluster}"

echo "Configuring gcloud for project ${PROJECT_ID}..."
gcloud config set project "${PROJECT_ID}"

echo "Enabling necessary Google Cloud APIs..."
gcloud services enable \
    container.googleapis.com \
    artifactregistry.googleapis.com \
    cloudbuild.googleapis.com \
    aiplatform.googleapis.com \
    cloudtrace.googleapis.com \
    monitoring.googleapis.com \
    telemetry.googleapis.com \
    --project="${PROJECT_ID}"

echo "Creating GKE cluster ${CLUSTER_NAME} with GKE Managed OpenTelemetry..."
if ! gcloud container clusters describe "${CLUSTER_NAME}" --zone="${ZONE}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
    gcloud container clusters create "${CLUSTER_NAME}" \
        --project="${PROJECT_ID}" \
        --zone="${ZONE}" \
        --release-channel=regular \
        --num-nodes=3 \
        --machine-type=e2-standard-4 \
        --image-type=COS_CONTAINERD \
        --workload-pool="${PROJECT_ID}.svc.id.goog" \
        --managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS \
        --scopes=cloud-platform,gke-default
else
    echo "Cluster ${CLUSTER_NAME} already exists."
fi

echo "Fetching cluster credentials..."
gcloud container clusters get-credentials "${CLUSTER_NAME}" --zone="${ZONE}" --project="${PROJECT_ID}"

echo "Creating dedicated namespaces..."
kubectl create namespace ai-agent --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace otel-system --dry-run=client -o yaml | kubectl apply -f -

echo "Milestone 1 cluster setup completed successfully."
