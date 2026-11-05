package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type wireRequest struct {
	Model         string        `json:"model"`
	Messages      []Message     `json:"messages"`
	Tools         []Tool        `json:"tools,omitempty"`
	Stream        bool          `json:"stream"`
	StreamOptions streamOptions `json:"stream_options"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chunk struct {
	Choices []struct {
		Delta struct {
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

const maxLine = 4 << 20

// Stream sends req with stream set, calls onText for each text delta and
// returns the assembled message. An error from onText stops the stream and
// is returned as is.
func (c *Client) Stream(ctx context.Context, req Request, h http.Header, onText func(string) error) (Response, error) {
	body, err := json.Marshal(wireRequest{
		Model: req.Model, Messages: req.Messages, Tools: req.Tools,
		Stream: true, StreamOptions: streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return Response{}, fmt.Errorf("transponder: %w", err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.URL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("transponder: %w", err)
	}
	for k, v := range h {
		hr.Header[k] = v
	}
	if hr.Header.Get("Authorization") == "" && c.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "text/event-stream")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(hr)
	if err != nil {
		return Response{}, fmt.Errorf("transponder: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return Response{}, statusError(resp)
	}

	res := Response{Message: Message{Role: "assistant"}}
	var content strings.Builder
	var calls []ToolCall
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var ch chunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return Response{}, fmt.Errorf("transponder: bad stream chunk: %w", err)
		}
		if ch.Usage != nil {
			res.InputTokens, res.OutputTokens = ch.Usage.PromptTokens, ch.Usage.CompletionTokens
		}
		for _, choice := range ch.Choices {
			if t := choice.Delta.Content; t != "" {
				content.WriteString(t)
				if err := onText(t); err != nil {
					return Response{}, err
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				for len(calls) <= tc.Index {
					calls = append(calls, ToolCall{})
				}
				call := &calls[tc.Index]
				if tc.ID != "" {
					call.ID = tc.ID
				}
				if tc.Function.Name != "" {
					call.Name = tc.Function.Name
				}
				if tc.Function.Arguments != nil {
					call.Arguments += *tc.Function.Arguments
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Response{}, fmt.Errorf("transponder: reading stream: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if in, out, ok := trailerUsage(resp.Trailer); ok {
		res.InputTokens, res.OutputTokens = in, out
	}
	res.Message.Content = content.String()
	res.Message.ToolCalls = calls
	return res, nil
}

func trailerUsage(t http.Header) (int, int, bool) {
	in, err1 := strconv.Atoi(t.Get("X-Usage-Input-Tokens"))
	out, err2 := strconv.Atoi(t.Get("X-Usage-Output-Tokens"))
	return in, out, err1 == nil && err2 == nil
}

func statusError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	if msg == "" {
		return errors.New("transponder: " + resp.Status)
	}
	return fmt.Errorf("transponder: %s: %s", resp.Status, msg)
}
