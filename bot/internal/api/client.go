// Package api is a client for the gateway's private bot endpoints (S11).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client talks to the gateway API authenticated with the shared service token.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// Credentials is the response of POST /bot/credentials (ТЗ §9).
type Credentials struct {
	ResetURL  string `json:"reset_url"`
	ExpiresIn int    `json:"expires_in"`
}

// User is the response of GET /bot/users/{id}.
type User struct {
	TelegramUserID int64      `json:"telegram_user_id"`
	ID             string     `json:"id"`
	Email          string     `json:"email"`
	FirstName      string     `json:"first_name"`
	LastName       string     `json:"last_name"`
	Telegram       string     `json:"telegram"`
	IsActive       bool       `json:"is_active"`
	ExpiresAt      *time.Time `json:"expires_at"`
}

// APIError carries the gateway status code of a failed request.
type APIError struct {
	Status int
	Body   string
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("gateway api: status %d: %s", e.Status, e.Body)
}

// NewClient builds a client authenticated with the shared service token.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// IssueCredentials requests a fresh password for the given Telegram handle.
func (c *Client) IssueCredentials(ctx context.Context, telegram string) (*Credentials, error) {
	id, err := strconv.ParseInt(telegram, 10, 64)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("invalid Telegram user ID")
	}
	body, err := json.Marshal(map[string]int64{"telegram_user_id": id})
	if err != nil {
		return nil, err
	}
	var out Credentials
	if err := c.do(ctx, http.MethodPost, "/bot/credentials", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetUser resolves a user by id so notifications reach the right chat.
func (c *Client) GetUser(ctx context.Context, id string) (*User, error) {
	var out User
	if err := c.do(ctx, http.MethodGet, "/bot/users/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	return nil
}

func (c *Client) BindTelegram(ctx context.Context, id int64, code string) error {
	body, err := json.Marshal(map[string]any{"telegram_user_id": id, "code": code})
	if err != nil {
		return err
	}
	var out struct {
		Linked bool `json:"linked"`
	}
	return c.do(ctx, http.MethodPost, "/bot/link", body, &out)
}
