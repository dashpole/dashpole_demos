// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package decoders

import (
	"encoding/json"
	"strings"
)

// VectorQueryDetails holds attributes extracted from Vector DB queries.
type VectorQueryDetails struct {
	System         string
	CollectionName string
	Operation      string
	VectorDim      int
	TopK           int
	ScoreThreshold float64
	HasFilter      bool
}

type qdrantSearchPayload struct {
	Vector         interface{} `json:"vector"`
	Limit          int         `json:"limit"`
	ScoreThreshold float64     `json:"score_threshold,omitempty"`
	Filter         interface{} `json:"filter,omitempty"`
}

// ParseVectorDBRequest inspects HTTP path and body to extract Vector DB semantic conventions.
func ParseVectorDBRequest(path string, body []byte) (*VectorQueryDetails, bool) {
	// Strip query parameters if present
	cleanPath := path
	if idx := strings.Index(cleanPath, "?"); idx != -1 {
		cleanPath = cleanPath[:idx]
	}
	cleanPath = strings.TrimSuffix(cleanPath, "/")

	// Qdrant pattern: /collections/{name}/points/search
	if strings.HasPrefix(cleanPath, "/collections/") {
		remainder := strings.TrimPrefix(cleanPath, "/collections/")
		if remainder == "" {
			return nil, false
		}
		parts := strings.Split(remainder, "/")
		if len(parts) >= 1 && parts[0] != "" {
			collection := parts[0]
			details := &VectorQueryDetails{
				System:         "qdrant",
				CollectionName: collection,
				Operation:      "query",
			}

			if len(parts) >= 3 && parts[1] == "points" && parts[2] == "search" {
				details.Operation = "search"
				var searchReq qdrantSearchPayload
				if err := json.Unmarshal(body, &searchReq); err == nil {
					details.TopK = searchReq.Limit
					details.ScoreThreshold = searchReq.ScoreThreshold
					if searchReq.Filter != nil {
						details.HasFilter = true
					}
					// Check vector dimension (dense array or named vector map)
					if vecList, ok := searchReq.Vector.([]interface{}); ok {
						details.VectorDim = len(vecList)
					} else if vecMap, ok := searchReq.Vector.(map[string]interface{}); ok {
						if vecList, ok := vecMap["vector"].([]interface{}); ok {
							details.VectorDim = len(vecList)
						} else if vecList, ok := vecMap["values"].([]interface{}); ok {
							details.VectorDim = len(vecList)
						}
					}
				}
				return details, true
			} else if len(parts) >= 2 && parts[1] == "points" {
				details.Operation = "upsert"
				return details, true
			}

			return details, true
		}
	}

	return nil, false
}
