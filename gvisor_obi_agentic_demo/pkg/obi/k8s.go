// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package obi

import (
	"sync"
)

// PodMetadata holds Kubernetes entity details for a container.
type PodMetadata struct {
	PodName       string
	Namespace     string
	ContainerName string
	NodeName      string
	PodUID        string
}

// K8sDecorator decorates spans with Kubernetes resource attributes.
type K8sDecorator struct {
	mu         sync.RWMutex
	containers map[string]PodMetadata
	defaultNode string
}

// NewK8sDecorator creates a new decorator instance.
func NewK8sDecorator(defaultNode string) *K8sDecorator {
	return &K8sDecorator{
		containers:  make(map[string]PodMetadata),
		defaultNode: defaultNode,
	}
}

// RegisterContainer registers metadata for a container ID.
func (kd *K8sDecorator) RegisterContainer(containerID string, meta PodMetadata) {
	kd.mu.Lock()
	defer kd.mu.Unlock()
	if meta.NodeName == "" {
		meta.NodeName = kd.defaultNode
	}
	kd.containers[containerID] = meta
}

// UnregisterContainer removes metadata for a container ID.
func (kd *K8sDecorator) UnregisterContainer(containerID string) {
	kd.mu.Lock()
	defer kd.mu.Unlock()
	delete(kd.containers, containerID)
}

// DecorateSpan enriches a span with Kubernetes metadata if available.
func (kd *K8sDecorator) DecorateSpan(span *TraceSpan) {
	kd.mu.RLock()
	meta, exists := kd.containers[span.ContainerID]
	kd.mu.RUnlock()

	if !exists {
		// Fallback attribute
		span.Attributes["k8s.container.id"] = span.ContainerID
		return
	}

	span.Attributes["k8s.pod.name"] = meta.PodName
	span.Attributes["k8s.namespace.name"] = meta.Namespace
	span.Attributes["k8s.container.name"] = meta.ContainerName
	span.Attributes["k8s.node.name"] = meta.NodeName
	span.Attributes["k8s.pod.uid"] = meta.PodUID
	span.Attributes["k8s.container.id"] = span.ContainerID
}
