package inference

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Message is one chat turn, in the OpenAI shape the backend expects.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Result is the accounting for a completed stream.
type Result struct {
	PromptTokens     int
	CompletionTokens int
}

// ErrModelNotServed is returned when a job asks for a model this node does not
// have. The agent turns it into a job.reject with reason "no_model" so the
// gateway can re-offer elsewhere immediately.
var ErrModelNotServed = fmt.Errorf("model not served by this node")

// Serves reports whether this node advertises `model`.
func (p *Proxy) Serves(model string) bool {
	_, ok := p.ref(model)
	return ok
}

// Stream runs a chat completion against the local backend and calls onDelta for
// each token chunk as it arrives.
//
// Deltas are surfaced as they are produced rather than after the response
// completes: the whole point of the agent protocol is that tokens reach the end
// user while the model is still generating.
func (p *Proxy) Stream(
	ctx context.Context,
	model string,
	messages []Message,
	maxTokens int,
	onDelta func(string) error,
) (Result, error) {
	var out Result
	// The gateway asks for a network id; the engine wants its own reference.
	engineRef, ok := p.ref(model)
	if !ok {
		return out, ErrModelNotServed
	}
	if len(messages) == 0 {
		return out, fmt.Errorf("at least one message is required")
	}
	if maxTokens <= 0 {
		maxTokens = 512
	}

	payload, err := json.Marshal(map[string]any{
		"model":      engineRef,
		"messages":   messages,
		"max_tokens": maxTokens,
		"stream":     true,
	})
	if err != nil {
		return out, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.backendURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := p.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("inference backend unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("inference backend returned %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var evt struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		// A frame we cannot parse is skipped rather than fatal — backends emit
		// keep-alives and vendor-specific events we have no interest in.
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}
		if evt.Usage != nil {
			out.PromptTokens = evt.Usage.PromptTokens
			out.CompletionTokens = evt.Usage.CompletionTokens
		}
		if len(evt.Choices) > 0 {
			if delta := evt.Choices[0].Delta.Content; delta != "" {
				if err := onDelta(delta); err != nil {
					return out, err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("inference stream failed: %w", err)
	}
	return out, ctx.Err()
}
