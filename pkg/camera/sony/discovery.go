package sony

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	ssdpAddr         = "239.255.255.250:1900"
	ssdpSearchTarget = "urn:schemas-sony-com:service:ScalarWebAPI:1"
)

// FallbackBaseURLs are the action-list URLs Sony bodies use on their own
// access point, probed when SSDP is unavailable (multicast is often filtered
// by VPNs and firewalls). Interchangeable-lens bodies such as the a5000 use the
// first; the QX lens cameras use the second.
var FallbackBaseURLs = []string{
	"http://192.168.122.1:8080/sony",
	"http://10.0.0.1:10000/sony",
}

// Device describes a discovered camera.
type Device struct {
	// FriendlyName is the model as advertised over UPnP, e.g. "ILCE-5000".
	FriendlyName string `json:"friendlyName"`
	// BaseURL is the JSON-RPC action-list URL, e.g. http://192.168.122.1:8080/sony.
	BaseURL string `json:"baseUrl"`
	// Services lists the API services the camera advertised.
	Services []string `json:"services,omitempty"`
}

// deviceDescription mirrors the parts of DmsRmtDesc.xml focal needs. Go's XML
// decoder matches local names when a tag carries no namespace, so the "av:"
// prefixed Sony elements bind without spelling out their namespace.
type deviceDescription struct {
	Device struct {
		FriendlyName string `xml:"friendlyName"`
		DeviceInfo   struct {
			Services []struct {
				Type string `xml:"X_ScalarWebAPI_ServiceType"`
				URL  string `xml:"X_ScalarWebAPI_ActionList_URL"`
			} `xml:"X_ScalarWebAPI_ServiceList>X_ScalarWebAPI_Service"`
		} `xml:"X_ScalarWebAPI_DeviceInfo"`
	} `xml:"device"`
}

// ParseDescription decodes a UPnP device description and extracts the camera
// service endpoint. It fails if the device offers no "camera" service.
func ParseDescription(r io.Reader) (Device, error) {
	var desc deviceDescription
	if err := xml.NewDecoder(r).Decode(&desc); err != nil {
		return Device{}, fmt.Errorf("decoding device description: %w", err)
	}
	dev := Device{FriendlyName: strings.TrimSpace(desc.Device.FriendlyName)}
	for _, svc := range desc.Device.DeviceInfo.Services {
		dev.Services = append(dev.Services, svc.Type)
		if svc.Type == ServiceCamera {
			dev.BaseURL = NormalizeBaseURL(svc.URL)
		}
	}
	if dev.BaseURL == "" {
		return Device{}, errors.New("device description has no camera service")
	}
	return dev, nil
}

// parseLocation extracts the LOCATION header from a raw SSDP response.
func parseLocation(resp []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(resp))
	for sc.Scan() {
		name, value, ok := strings.Cut(sc.Text(), ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "location") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Discover finds a camera on the local network. It multicasts an SSDP search
// for Sony's ScalarWebAPI service and reads the first responder's device
// description; if nothing answers before ctx's deadline (or within timeout),
// it probes FallbackBaseURLs directly.
func Discover(ctx context.Context, hc *http.Client, timeout time.Duration) (Device, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	ssdpCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dev, ssdpErr := discoverSSDP(ssdpCtx, hc)
	if ssdpErr == nil {
		return dev, nil
	}
	if ctx.Err() != nil {
		return Device{}, ctx.Err()
	}
	if dev, err := Probe(ctx, hc, FallbackBaseURLs...); err == nil {
		return dev, nil
	}
	return Device{}, fmt.Errorf("no camera found (SSDP: %v). Is the laptop joined to the camera's Wi-Fi and the Smart Remote app running?", ssdpErr)
}

func discoverSSDP(ctx context.Context, hc *http.Client) (Device, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return Device{}, fmt.Errorf("opening UDP socket: %w", err)
	}
	defer conn.Close()
	dst, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return Device{}, err
	}

	msg := []byte("M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + ssdpAddr + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 1\r\n" +
		"ST: " + ssdpSearchTarget + "\r\n\r\n")

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(3 * time.Second)
	}
	// UDP is lossy: resend the search every half second until something answers.
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) {
		if _, err := conn.WriteTo(msg, dst); err != nil {
			return Device{}, fmt.Errorf("sending SSDP search: %w", err)
		}
		_ = conn.SetReadDeadline(minTime(deadline, time.Now().Add(500*time.Millisecond)))
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				break // read deadline: resend
			}
			loc := parseLocation(buf[:n])
			if loc == "" {
				continue
			}
			if dev, err := fetchDescription(ctx, hc, loc); err == nil {
				return dev, nil
			}
		}
	}
	return Device{}, errors.New("no SSDP response")
}

func fetchDescription(ctx context.Context, hc *http.Client, location string) (Device, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return Device{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Device{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Device{}, fmt.Errorf("fetching %s: %s", location, resp.Status)
	}
	return ParseDescription(io.LimitReader(resp.Body, 1<<20))
}

// Probe returns the first base URL whose camera service answers getVersions.
func Probe(ctx context.Context, hc *http.Client, baseURLs ...string) (Device, error) {
	var lastErr error = errors.New("no candidate URLs")
	for _, base := range baseURLs {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := NewClient(base, hc).Call(pctx, ServiceCamera, "getVersions", nil, "1.0")
		cancel()
		if err == nil {
			return Device{BaseURL: NormalizeBaseURL(base), Services: []string{ServiceCamera}}, nil
		}
		lastErr = err
	}
	return Device{}, lastErr
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
