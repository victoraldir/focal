// Package sony talks to Sony cameras through the Camera Remote API: a JSON-RPC
// dialect over HTTP served by the camera's built-in "Smart Remote" app while it
// hosts its own Wi-Fi network. It covers the bodies of the a5000/a6000/NEX era
// (and the QX lens-style cameras), discovers them over SSDP, and implements
// domain.Camera so the interval Shooter can drive them.
//
// The package is layered: Client speaks raw JSON-RPC, Camera wraps it in typed
// operations (shoot, settings, zoom, live view), and LiveviewReader decodes the
// camera's proprietary live-view packet stream into JPEG frames.
package sony

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

// Well-known Camera Remote API error codes. The full table lives in Sony's API
// reference; these are the ones focal reacts to.
const (
	ErrCodeAny                = 1
	ErrCodeIllegalArgument    = 3
	ErrCodeNoSuchMethod       = 12
	ErrCodeUnsupportedVersion = 14
	ErrCodeShootingFail       = 40400
	ErrCodeCameraNotReady     = 40401
	ErrCodeAlreadyPolling     = 40402
	ErrCodeStillCapturing     = 40403
)

// Services exposed by the camera. Each is a separate JSON-RPC endpoint under
// the action-list URL, e.g. http://192.168.122.1:8080/sony/camera.
const (
	ServiceCamera    = "camera"
	ServiceSystem    = "system"
	ServiceAVContent = "avContent"
	ServiceGuide     = "guide"
)

// APIError is an error object returned by the camera: {"error": [code, "msg"]}.
type APIError struct {
	Method  string
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("camera rejected %s: %s (code %d)", e.Method, e.Message, e.Code)
}

// IsAPIError reports whether err is an APIError with the given code.
func IsAPIError(err error, code int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// Client is a minimal JSON-RPC client bound to one camera's action-list URL.
type Client struct {
	base   string
	http   *http.Client
	nextID atomic.Int64
}

// NewClient returns a client for the action-list URL base (for example
// "http://192.168.122.1:8080/sony"). A trailing slash or service suffix such as
// "/camera" is tolerated. hc defaults to http.DefaultClient; timeouts are
// expected to come from the per-call context because some methods (event
// long-polling, long exposures) legitimately block for a long time.
func NewClient(base string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: NormalizeBaseURL(base), http: hc}
}

// BaseURL returns the action-list URL the client is bound to.
func (c *Client) BaseURL() string { return c.base }

// NormalizeBaseURL trims a trailing slash and a known service suffix so users
// can pass either ".../sony" or ".../sony/camera".
func NormalizeBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	for _, svc := range []string{ServiceCamera, ServiceSystem, ServiceAVContent, ServiceGuide} {
		if strings.HasSuffix(base, "/"+svc) {
			return strings.TrimSuffix(base, "/"+svc)
		}
	}
	return base
}

type rpcRequest struct {
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      int64  `json:"id"`
	Version string `json:"version"`
}

type rpcResponse struct {
	Result  json.RawMessage `json:"result"`
	Results json.RawMessage `json:"results"`
	Error   []any           `json:"error"`
}

// Call invokes method on service with positional params and returns the raw
// "result" array. version defaults to "1.0". Camera-side errors are returned as
// *APIError.
func (c *Client) Call(ctx context.Context, service, method string, params []any, version string) (json.RawMessage, error) {
	if params == nil {
		params = []any{}
	}
	if version == "" {
		version = "1.0"
	}
	body, err := json.Marshal(rpcRequest{Method: method, Params: params, ID: c.nextID.Add(1), Version: version})
	if err != nil {
		return nil, fmt.Errorf("encoding %s request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+service, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("reading %s response: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("calling %s: unexpected HTTP status %s", method, resp.Status)
	}

	var out rpcResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decoding %s response: %w", method, err)
	}
	if len(out.Error) > 0 {
		return nil, parseAPIError(method, out.Error)
	}
	if len(out.Result) > 0 {
		return out.Result, nil
	}
	// A handful of methods (getMethodTypes) answer with "results" instead.
	return out.Results, nil
}

func parseAPIError(method string, fields []any) *APIError {
	e := &APIError{Method: method, Message: "unknown error"}
	if code, ok := fields[0].(float64); ok {
		e.Code = int(code)
	}
	if len(fields) > 1 {
		if msg, ok := fields[1].(string); ok && msg != "" {
			e.Message = msg
		}
	}
	return e
}
