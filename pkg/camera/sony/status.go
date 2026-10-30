package sony

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Choice is a setting's current value and the values the camera accepts now.
type Choice struct {
	Current    string   `json:"current"`
	Candidates []string `json:"candidates,omitempty"`
}

// ExposureCompensation is expressed in steps: the EV value is Current×Step.
type ExposureCompensation struct {
	Current int     `json:"current"`
	Min     int     `json:"min"`
	Max     int     `json:"max"`
	Step    float64 `json:"step"`
}

// Battery is the primary battery's charge.
type Battery struct {
	// Percent is 0–100, or -1 when the camera does not report a level.
	Percent int    `json:"percent"`
	Status  string `json:"status,omitempty"`
}

// Storage is the active memory card.
type Storage struct {
	ID string `json:"id,omitempty"`
	// RemainingShots is -1 when unknown or when no card is inserted.
	RemainingShots int `json:"remainingShots"`
}

// Status is a normalised snapshot of the camera's event feed. Fields the body
// does not report are left nil so clients can hide the matching controls.
type Status struct {
	CameraStatus         string                `json:"cameraStatus"`
	AvailableAPIs        []string              `json:"availableApis"`
	LiveviewActive       bool                  `json:"liveviewActive"`
	ZoomPosition         *int                  `json:"zoomPosition,omitempty"`
	FocusStatus          string                `json:"focusStatus,omitempty"`
	ShootMode            *Choice               `json:"shootMode,omitempty"`
	ExposureMode         *Choice               `json:"exposureMode,omitempty"`
	FNumber              *Choice               `json:"fNumber,omitempty"`
	ShutterSpeed         *Choice               `json:"shutterSpeed,omitempty"`
	ISO                  *Choice               `json:"isoSpeedRate,omitempty"`
	WhiteBalance         *Choice               `json:"whiteBalance,omitempty"`
	FocusMode            *Choice               `json:"focusMode,omitempty"`
	FlashMode            *Choice               `json:"flashMode,omitempty"`
	SelfTimer            *Choice               `json:"selfTimer,omitempty"`
	PostviewImageSize    *Choice               `json:"postviewImageSize,omitempty"`
	ExposureCompensation *ExposureCompensation `json:"exposureCompensation,omitempty"`
	Battery              *Battery              `json:"battery,omitempty"`
	Storage              *Storage              `json:"storage,omitempty"`
}

// eventVersions are tried newest-first; later versions report more fields
// (focus status, newer settings) but older bodies reject them.
var eventVersions = []string{"1.2", "1.1", "1.0"}

// Event returns the raw getEvent result without long-polling, negotiating the
// newest event version the body supports.
func (c *Camera) Event(ctx context.Context) (json.RawMessage, error) {
	c.mu.Lock()
	known := c.eventVersion
	c.mu.Unlock()

	versions := eventVersions
	if known != "" {
		versions = []string{known}
	}
	var lastErr error
	for _, v := range versions {
		cctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
		raw, err := c.client.Call(cctx, ServiceCamera, "getEvent", []any{false}, v)
		cancel()
		if err == nil {
			c.mu.Lock()
			c.eventVersion = v
			c.mu.Unlock()
			return raw, nil
		}
		lastErr = err
		if !IsAPIError(err, ErrCodeUnsupportedVersion) && !IsAPIError(err, ErrCodeNoSuchMethod) {
			return nil, err
		}
	}
	return nil, lastErr
}

// Status fetches and normalises the camera's current state.
func (c *Camera) Status(ctx context.Context) (Status, error) {
	raw, err := c.Event(ctx)
	if err != nil {
		return Status{}, err
	}
	st, err := ParseEvent(raw)
	if err != nil {
		return Status{}, err
	}
	// White balance candidates are not part of the event feed; fetch them once.
	if st.WhiteBalance != nil && slices.Contains(st.AvailableAPIs, "getAvailableWhiteBalance") {
		st.WhiteBalance.Candidates = c.whiteBalanceCandidates(ctx)
	}
	return st, nil
}

func (c *Camera) whiteBalanceCandidates(ctx context.Context) []string {
	c.mu.Lock()
	cached := c.wbCandidates
	c.mu.Unlock()
	if cached != nil {
		return cached
	}
	raw, err := c.call(ctx, "getAvailableWhiteBalance")
	if err != nil {
		return nil
	}
	var res []json.RawMessage
	if json.Unmarshal(raw, &res) != nil || len(res) < 2 {
		return nil
	}
	var modes []struct {
		Mode string `json:"whiteBalanceMode"`
	}
	_ = json.Unmarshal(res[1], &modes)
	cands := make([]string, 0, len(modes))
	for _, m := range modes {
		cands = append(cands, m.Mode)
	}
	c.mu.Lock()
	c.wbCandidates = cands
	c.mu.Unlock()
	return cands
}

// ParseEvent normalises a getEvent result. The feed is a positional array whose
// layout differs between versions, but every populated slot is an object (or
// array of objects) tagged with a "type", so entries are indexed by that tag.
func ParseEvent(raw json.RawMessage) (Status, error) {
	var slots []json.RawMessage
	if err := json.Unmarshal(raw, &slots); err != nil {
		return Status{}, fmt.Errorf("decoding event: %w", err)
	}
	byType := map[string][]map[string]any{}
	for _, slot := range slots {
		var obj map[string]any
		if json.Unmarshal(slot, &obj) == nil && obj != nil {
			if t, _ := obj["type"].(string); t != "" {
				byType[t] = append(byType[t], obj)
			}
			continue
		}
		var arr []map[string]any
		if json.Unmarshal(slot, &arr) == nil {
			for _, o := range arr {
				if t, _ := o["type"].(string); t != "" {
					byType[t] = append(byType[t], o)
				}
			}
		}
	}
	first := func(t string) map[string]any {
		if objs := byType[t]; len(objs) > 0 {
			return objs[0]
		}
		return nil
	}

	var st Status
	if o := first("availableApiList"); o != nil {
		st.AvailableAPIs = toStrings(o["names"])
	}
	if o := first("cameraStatus"); o != nil {
		st.CameraStatus = toString(o["cameraStatus"])
	}
	if o := first("liveviewStatus"); o != nil {
		st.LiveviewActive, _ = o["liveviewStatus"].(bool)
	}
	if o := first("zoomInformation"); o != nil {
		if p, ok := o["zoomPosition"].(float64); ok {
			n := int(p)
			st.ZoomPosition = &n
		}
	}
	if o := first("focusStatus"); o != nil {
		st.FocusStatus = toString(o["focusStatus"])
	}

	choices := []struct {
		dst      **Choice
		typ, cur string
		cands    string
	}{
		{&st.ShootMode, "shootMode", "currentShootMode", "shootModeCandidates"},
		{&st.ExposureMode, "exposureMode", "currentExposureMode", "exposureModeCandidates"},
		{&st.FNumber, "fNumber", "currentFNumber", "fNumberCandidates"},
		{&st.ShutterSpeed, "shutterSpeed", "currentShutterSpeed", "shutterSpeedCandidates"},
		{&st.ISO, "isoSpeedRate", "currentIsoSpeedRate", "isoSpeedRateCandidates"},
		{&st.WhiteBalance, "whiteBalance", "currentWhiteBalanceMode", ""},
		{&st.FocusMode, "focusMode", "currentFocusMode", "focusModeCandidates"},
		{&st.FlashMode, "flashMode", "currentFlashMode", "flashModeCandidates"},
		{&st.SelfTimer, "selfTimer", "currentSelfTimer", "selfTimerCandidates"},
		{&st.PostviewImageSize, "postviewImageSize", "currentPostviewImageSize", "postviewImageSizeCandidates"},
	}
	for _, ch := range choices {
		o := first(ch.typ)
		if o == nil {
			continue
		}
		cur, ok := o[ch.cur]
		if !ok {
			continue
		}
		*ch.dst = &Choice{Current: toString(cur), Candidates: toStrings(o[ch.cands])}
	}

	if o := first("exposureCompensation"); o != nil {
		ev := &ExposureCompensation{
			Current: toInt(o["currentExposureCompensation"]),
			Min:     toInt(o["minExposureCompensation"]),
			Max:     toInt(o["maxExposureCompensation"]),
			Step:    1.0 / 3,
		}
		// stepIndexOfExposureCompensation: 1 → 1/3 EV, 2 → 1/2 EV.
		if toInt(o["stepIndexOfExposureCompensation"]) == 2 {
			ev.Step = 0.5
		}
		st.ExposureCompensation = ev
	}
	if o := first("batteryInfo"); o != nil {
		b := &Battery{Percent: -1, Status: toString(o["status"])}
		if denom := toInt(o["levelDenom"]); denom > 0 {
			b.Percent = toInt(o["levelNumer"]) * 100 / denom
		}
		st.Battery = b
	}
	for _, o := range byType["storageInformation"] {
		if rt, _ := o["recordTarget"].(bool); rt || st.Storage == nil {
			st.Storage = &Storage{ID: toString(o["storageID"]), RemainingShots: toInt(o["numberOfRecordableImages"])}
		}
	}
	return st, nil
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func toStrings(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, toString(item))
	}
	return out
}

func toInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	default:
		return 0
	}
}

// scalarString renders a JSON scalar (string or number) as a plain string.
func scalarString(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return strings.Trim(string(raw), `"`)
	}
	return toString(v)
}
