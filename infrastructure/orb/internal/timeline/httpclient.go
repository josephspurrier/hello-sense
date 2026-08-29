package timeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPClient talks to the stateless Java algorithm service.
//
// JSON rather than protobuf on this hop deliberately: the payload is a few
// hundred KB once a night, so encoding cost is irrelevant, and being able to
// read a request by eye is worth a great deal when the thing on the other end
// is a black box whose output you cannot independently verify.
type HTTPClient struct {
	url string
	hc  *http.Client
}

func NewHTTPClient(url string, timeout time.Duration) *HTTPClient {
	return &HTTPClient{url: url, hc: &http.Client{Timeout: timeout}}
}

func (c *HTTPClient) Timeline(ctx context.Context, req Request) (Result, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, fmt.Errorf("timeline: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/timeline", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("timeline: call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Include a snippet of the body: the Java side reports algorithm
		// failures as text, and losing that turns a diagnosable problem into
		// "the service returned 500".
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Result{}, fmt.Errorf("timeline: %s: %s", resp.Status, bytes.TrimSpace(snippet))
	}

	var out Result
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Result{}, fmt.Errorf("timeline: decode result: %w", err)
	}
	return out, nil
}
