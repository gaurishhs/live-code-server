package eventapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrInvalidPassword = errors.New("invalid event password")
	ErrSlotOccupied    = errors.New("the livecode sharing slot is already in use")
	ErrLeaseExpired    = errors.New("the livecode sharing lease is no longer valid")
)

type ClaimResponse struct {
	Lease       string `json:"lease"`
	ExpiresAt   int64  `json:"expiresAt"`
	TunnelToken string `json:"tunnelToken"`
	PublicURL   string `json:"publicUrl"`
}

type RenewResponse struct {
	OK        bool  `json:"ok"`
	ExpiresAt int64 `json:"expiresAt"`
}

type StatusError struct {
	Operation string
	Status    int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("event service %s failed (HTTP %d)", e.Operation, e.Status)
}

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string, client *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("event API URL must be an absolute HTTP or HTTPS URL")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(u.String(), "/"), http: client}, nil
}

func (c *Client) Claim(ctx context.Context, password string) (ClaimResponse, error) {
	var out ClaimResponse
	err := c.post(ctx, "/claim", password, &out, "claim")
	if err != nil {
		return ClaimResponse{}, err
	}
	if out.Lease == "" || out.TunnelToken == "" || out.PublicURL == "" || out.ExpiresAt <= 0 {
		return ClaimResponse{}, errors.New("event service returned an incomplete claim response")
	}
	return out, nil
}

func (c *Client) Renew(ctx context.Context, lease string) (RenewResponse, error) {
	var out RenewResponse
	err := c.post(ctx, "/renew", lease, &out, "renew")
	if err != nil {
		return RenewResponse{}, err
	}
	if !out.OK || out.ExpiresAt <= 0 {
		return RenewResponse{}, errors.New("event service returned an invalid renewal response")
	}
	return out, nil
}

func (c *Client) Release(ctx context.Context, lease string) error {
	return c.post(ctx, "/release", lease, nil, "release")
}

func (c *Client) post(ctx context.Context, endpoint, credential string, out any, operation string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, bytes.NewReader(nil))
	if err != nil {
		return fmt.Errorf("create event service request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("unable to contact livecode event service: %w", safeNetworkError(err))
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		switch {
		case operation == "claim" && res.StatusCode == http.StatusUnauthorized:
			return ErrInvalidPassword
		case operation == "claim" && res.StatusCode == http.StatusConflict:
			return ErrSlotOccupied
		case operation == "renew" && res.StatusCode == http.StatusUnauthorized:
			return ErrLeaseExpired
		default:
			return &StatusError{Operation: operation, Status: res.StatusCode}
		}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(res.Body, 64*1024))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("event service returned malformed %s response", operation)
	}
	return nil
}

// Avoid propagating transport messages that may include request URLs or headers.
func safeNetworkError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		if uerr.Timeout() {
			return errors.New("request timed out")
		}
		return errors.New("network request failed")
	}
	return errors.New("network request failed")
}
