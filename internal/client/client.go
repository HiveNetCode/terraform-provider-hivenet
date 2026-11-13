// Package client is a minimal client for the Hivenet Compute public API.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxRetries        = 5
	defaultRetryAfter = 5 * time.Second
	pageSize          = 200
)

// Client calls the Hivenet Compute public API.
type Client struct {
	endpoint   string
	token      string
	userAgent  string
	httpClient *http.Client
	// sleep is swapped in tests so 429 retries don't wait.
	sleep func(context.Context, time.Duration) error
}

// New returns a client for endpoint, e.g. https://staging-api.hivecompute.ai/v1.
func New(endpoint, token, userAgent string) *Client {
	return &Client{
		endpoint:   strings.TrimRight(endpoint, "/"),
		token:      token,
		userAgent:  userAgent,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		sleep:      sleepCtx,
	}
}

// Instance is the subset of the API's Instance schema the provider uses.
type Instance struct {
	ID                string             `json:"instance_id"`
	Name              string             `json:"name"`
	Status            Status             `json:"status"`
	Region            string             `json:"region"`
	GPUType           string             `json:"gpu_type"`
	Size              string             `json:"size"`
	CPUCount          int64              `json:"cpu_count"`
	Resources         string             `json:"resources"`
	Bandwidth         float64            `json:"bandwidth"`
	StorageUsedGB     float64            `json:"storage_used_gb"`
	HourlyRate        float64            `json:"hourly_rate"`
	TotalCostIncurred float64            `json:"total_cost_incurred"`
	TotalRuntimeHours float64            `json:"total_runtime_hours"`
	TTLRemainingHours float64            `json:"ttl_remaining_hours"`
	StartedAt         *Timestamp         `json:"started_at"`
	TerminatedAt      *Timestamp         `json:"terminated_at"`
	TerminationReason *TerminationReason `json:"termination_reason"`
	GuestReady        GuestReady         `json:"guest_ready"`
	GuestReadyReason  string             `json:"guest_ready_reason"`
}

// ListInstancesOptions filters ListInstances. Zero values mean no filter.
type ListInstancesOptions struct {
	FreeTextSearch string
}

// Region is a region code from GET /regions, e.g. "FR".
type Region struct {
	Name string `json:"name"`
}

// Preset is a hardware configuration from GET /presets. The API sends every
// field as a string, numbers included.
type Preset struct {
	ID              string `json:"id"`
	Region          string `json:"region"`
	Location        string `json:"location"`
	GPU             string `json:"gpu"`
	CPU             string `json:"cpu"`
	Memory          string `json:"memory"`
	Storage         string `json:"storage"`
	HourlyPrice     string `json:"hourly_price"`
	PriceDiscounted string `json:"price_discounted"`
}

// ListPresetsOptions filters ListPresets. Zero values mean no filter.
type ListPresetsOptions struct {
	Region         string
	SKU            string
	FreeTextSearch string
}

// APIError is the API's error envelope plus the HTTP status.
type APIError struct {
	StatusCode int
	Code       string `json:"code"`
	Message    string `json:"message"`
	RequestID  string `json:"request_id"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("hivenet API: %d %s: %s (request_id %s)", e.StatusCode, e.Code, e.Message, e.RequestID)
}

// IsStatus reports whether err is an APIError with the given HTTP status.
func IsStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

// ListInstances returns every instance on the caller's account, across all pages.
func (c *Client) ListInstances(ctx context.Context, opts ListInstancesOptions) ([]Instance, error) {
	q := url.Values{}
	setIf(q, "free_text_search", opts.FreeTextSearch)
	return listAll[Instance](ctx, c, "/instances", q)
}

// GetInstance returns one instance. A missing or foreign ID is a 404 APIError.
func (c *Client) GetInstance(ctx context.Context, id string) (*Instance, error) {
	var inst Instance
	if err := c.do(ctx, http.MethodGet, "/instances/"+url.PathEscape(id), &inst); err != nil {
		return nil, err
	}
	return &inst, nil
}

// ListRegions returns the region codes the API accepts as filters.
func (c *Client) ListRegions(ctx context.Context) ([]Region, error) {
	var regions []Region
	if err := c.do(ctx, http.MethodGet, "/regions", &regions); err != nil {
		return nil, err
	}
	return regions, nil
}

// ListPresets returns every matching preset, across all pages.
func (c *Client) ListPresets(ctx context.Context, opts ListPresetsOptions) ([]Preset, error) {
	q := url.Values{}
	setIf(q, "region", opts.Region)
	setIf(q, "sku", opts.SKU)
	setIf(q, "free_text_search", opts.FreeTextSearch)
	return listAll[Preset](ctx, c, "/presets", q)
}

// listAll follows the cursor of a paginated list endpoint to the last page.
func listAll[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	var all []T
	q.Set("size", strconv.Itoa(pageSize))
	for {
		var page struct {
			Data       []T `json:"data"`
			Pagination struct {
				Next *string `json:"next"`
			} `json:"pagination"`
		}
		if err := c.do(ctx, http.MethodGet, path+"?"+q.Encode(), &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if page.Pagination.Next == nil || *page.Pagination.Next == "" {
			return all, nil
		}
		q.Set("cursor", *page.Pagination.Next)
	}
}

func setIf(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

// TerminateInstance asks the API to terminate an instance. It does not wait.
func (c *Client) TerminateInstance(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/instances/"+url.PathEscape(id)+"/terminate", nil)
}

// StartInstance asks the API to start a stopped instance. It does not wait.
// A terminated instance is a 409 APIError.
func (c *Client) StartInstance(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/instances/"+url.PathEscape(id)+"/start", nil)
}

// StopInstance asks the API to stop a running instance. It does not wait.
// A terminated instance is a 409 APIError.
func (c *Client) StopInstance(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/instances/"+url.PathEscape(id)+"/stop", nil)
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			if err := c.sleep(ctx, retryAfter(resp.Header.Get("Retry-After"))); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode >= 400 {
			apiErr := &APIError{StatusCode: resp.StatusCode}
			var envelope struct {
				Error *APIError `json:"error"`
			}
			envelope.Error = apiErr
			if json.Unmarshal(body, &envelope) != nil || apiErr.Code == "" {
				apiErr.Message = strings.TrimSpace(string(body))
			}
			return apiErr
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(body, out)
	}
}

func retryAfter(header string) time.Duration {
	if secs, err := strconv.Atoi(header); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return defaultRetryAfter
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
