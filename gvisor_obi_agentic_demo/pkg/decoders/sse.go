// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package decoders

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// SSEStreamingMetrics captures GenAI LLM token generation metrics.
type SSEStreamingMetrics struct {
	ModelName      string
	PromptTokens   int
	OutputTokens   int
	TotalTokens    int
	TimeToFirstTok time.Duration
	FullText       strings.Builder
	IsDone         bool
	ChunkCount     int
}

// SSEDecoder parses Server-Sent Events emitted by OpenAI, Vertex AI, and Anthropic APIs.
type SSEDecoder struct {
	mu             sync.Mutex
	reqStartTime   time.Time
	firstChunkTime time.Time
	metrics        SSEStreamingMetrics
}

// NewSSEDecoder initializes a new SSE decoder with the request start timestamp.
func NewSSEDecoder(reqStartTime time.Time) *SSEDecoder {
	if reqStartTime.IsZero() {
		reqStartTime = time.Now()
	}
	return &SSEDecoder{
		reqStartTime: reqStartTime,
	}
}

// IngestChunk parses incoming SSE data buffer.
func (d *SSEDecoder) IngestChunk(chunk []byte, arrivalTime time.Time) *SSEStreamingMetrics {
	d.mu.Lock()
	defer d.mu.Unlock()

	if arrivalTime.IsZero() {
		arrivalTime = time.Now()
	}

	scanner := bufio.NewScanner(bytes.NewReader(chunk))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		dataPayload := strings.TrimSpace(line[5:])
		if dataPayload == "[DONE]" {
			d.metrics.IsDone = true
			continue
		}

		if dataPayload == "" {
			continue
		}

		if d.firstChunkTime.IsZero() {
			d.firstChunkTime = arrivalTime
			d.metrics.TimeToFirstTok = d.firstChunkTime.Sub(d.reqStartTime)
		}

		d.metrics.ChunkCount++
		d.parseJSONData([]byte(dataPayload))
	}

	return &d.metrics
}

// parseJSONData parses the payload structure across providers.
func (d *SSEDecoder) parseJSONData(raw []byte) {
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	// 1. Model Name
	if d.metrics.ModelName == "" {
		if model, ok := payload["model"].(string); ok && model != "" {
			d.metrics.ModelName = model
		} else if modelVer, ok := payload["modelVersion"].(string); ok && modelVer != "" {
			d.metrics.ModelName = modelVer
		}
	}

	// 2. OpenAI / Compatible delta
	if choices, ok := payload["choices"].([]interface{}); ok && len(choices) > 0 {
		if firstChoice, ok := choices[0].(map[string]interface{}); ok {
			if delta, ok := firstChoice["delta"].(map[string]interface{}); ok {
				if content, ok := delta["content"].(string); ok {
					d.metrics.FullText.WriteString(content)
					d.metrics.OutputTokens += estimateTokens(content)
				}
			}
		}
	}

	// 3. Vertex AI / Gemini candidates
	if candidates, ok := payload["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if firstCand, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := firstCand["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
					if firstPart, ok := parts[0].(map[string]interface{}); ok {
						if text, ok := firstPart["text"].(string); ok {
							d.metrics.FullText.WriteString(text)
							d.metrics.OutputTokens += estimateTokens(text)
						}
					}
				}
			}
		}
	}

	// 4. Anthropic delta
	if delta, ok := payload["delta"].(map[string]interface{}); ok {
		if text, ok := delta["text"].(string); ok {
			d.metrics.FullText.WriteString(text)
			d.metrics.OutputTokens += estimateTokens(text)
		}
	} else if contentBlock, ok := payload["content_block"].(map[string]interface{}); ok {
		if text, ok := contentBlock["text"].(string); ok {
			d.metrics.FullText.WriteString(text)
			d.metrics.OutputTokens += estimateTokens(text)
		}
	}

	// 5. Usage metadata if present in stream (OpenAI / Anthropic)
	hasTotalTokens := false
	if usage, ok := payload["usage"].(map[string]interface{}); ok {
		if pt, ok := usage["prompt_tokens"].(float64); ok {
			d.metrics.PromptTokens = int(pt)
		} else if it, ok := usage["input_tokens"].(float64); ok {
			d.metrics.PromptTokens = int(it)
		}

		if ct, ok := usage["completion_tokens"].(float64); ok {
			d.metrics.OutputTokens = int(ct)
		} else if ot, ok := usage["output_tokens"].(float64); ok {
			d.metrics.OutputTokens = int(ot)
		}

		if tt, ok := usage["total_tokens"].(float64); ok {
			d.metrics.TotalTokens = int(tt)
			hasTotalTokens = true
		}
	}

	// 6. Usage metadata for Vertex AI / Gemini (usageMetadata)
	if usageMeta, ok := payload["usageMetadata"].(map[string]interface{}); ok {
		if pt, ok := usageMeta["promptTokenCount"].(float64); ok {
			d.metrics.PromptTokens = int(pt)
		}
		if ct, ok := usageMeta["candidatesTokenCount"].(float64); ok {
			d.metrics.OutputTokens = int(ct)
		}
		if tt, ok := usageMeta["totalTokenCount"].(float64); ok {
			d.metrics.TotalTokens = int(tt)
			hasTotalTokens = true
		}
	}

	if !hasTotalTokens {
		d.metrics.TotalTokens = d.metrics.PromptTokens + d.metrics.OutputTokens
	}
}

// estimateTokens returns estimated token count for text chunk.
func estimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	words := len(strings.Fields(text))
	if words > 0 {
		return words
	}
	return (len(text) + 3) / 4
}

// Metrics returns the accumulated metrics.
func (d *SSEDecoder) Metrics() *SSEStreamingMetrics {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &d.metrics
}
