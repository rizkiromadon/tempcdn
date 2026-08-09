package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const purgeEndpointFmt = "https://api.cloudflare.com/client/v4/zones/%s/purge_cache"

type Purger interface {
	PurgeURLs(ctx context.Context, urls []string) error
}

type Client struct {
	zoneID     string
	apiToken   string
	httpClient *http.Client
}

func NewClient(zoneID, apiToken string) *Client {
	return &Client{
		zoneID:   zoneID,
		apiToken: apiToken,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type purgeRequestBody struct {
	Files []string `json:"files"`
}

type purgeResponseBody struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *Client) PurgeURLs(ctx context.Context, urls []string) error {
	if len(urls) == 0 {
		return nil
	}

	payload, err := json.Marshal(purgeRequestBody{Files: urls})
	if err != nil {
		return fmt.Errorf("marshal purge request: %w", err)
	}

	endpoint := fmt.Sprintf(purgeEndpointFmt, c.zoneID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build purge request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("purge cache request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read purge response: %w", err)
	}

	var parsed purgeResponseBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("decode purge response: %w", err)
	}

	if !parsed.Success {
		if len(parsed.Errors) > 0 {
			return fmt.Errorf("cloudflare purge failed: %s", parsed.Errors[0].Message)
		}
		return fmt.Errorf("cloudflare purge failed with status %d", resp.StatusCode)
	}

	return nil
}
