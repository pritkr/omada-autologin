package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return fmt.Errorf("expected a number, got %s", string(b))
	}
	*f = flexInt(n)
	return nil
}

type portalSetting struct {
	ErrorCode *flexInt        `json:"errorCode"`
	Msg       string          `json:"msg"`
	Result    json.RawMessage `json:"result"`
}

type settingResult struct {
	AuthType int `json:"authType"`
	Hotspot  struct {
		EnabledTypes []int `json:"enabledTypes"`
	} `json:"hotspot"`
}

type authMethod struct {
	Name     string
	AuthType int
	Endpoint string
}

// resolveAuthMethod maps Omada's authType to the endpoint and payload style.
// See the TP-Link portal template; only username/password methods are handled.
func resolveAuthMethod(r settingResult) (authMethod, error) {
	switch r.AuthType {
	case 2, 8:
		return authMethod{Name: "radius", AuthType: r.AuthType, Endpoint: "/portal/radius/auth"}, nil
	case 5:
		return authMethod{Name: "local", AuthType: 5, Endpoint: "/portal/auth"}, nil
	case 1:
		return authMethod{Name: "simple", AuthType: 1, Endpoint: "/portal/auth"}, nil
	case 0:
		return authMethod{Name: "none", AuthType: 0, Endpoint: "/portal/auth"}, nil
	case 11:
		// Hotspot mode can advertise several methods at once.
		for _, preferred := range []int{5, 8} {
			for _, enabled := range r.Hotspot.EnabledTypes {
				if enabled == preferred {
					if preferred == 5 {
						return authMethod{Name: "local", AuthType: 5, Endpoint: "/portal/auth"}, nil
					}
					return authMethod{Name: "radius", AuthType: 8, Endpoint: "/portal/radius/auth"}, nil
				}
			}
		}
		for _, enabled := range r.Hotspot.EnabledTypes {
			if enabled == 3 {
				return authMethod{}, errors.New("portal requires voucher authentication; this tool only handles username/password")
			}
			if enabled == 6 {
				return authMethod{}, errors.New("portal requires SMS authentication; this tool only handles username/password")
			}
		}
		return authMethod{}, fmt.Errorf("unsupported Hotspot methods: %v", r.Hotspot.EnabledTypes)
	case 3:
		return authMethod{}, errors.New("portal requires voucher authentication; this tool only handles username/password")
	case 6:
		return authMethod{}, errors.New("portal requires SMS authentication; this tool only handles username/password")
	default:
		return authMethod{}, fmt.Errorf("unsupported authType %d", r.AuthType)
	}
}

func (m authMethod) payload(cfg *Config, ctx *PortalContext) map[string]any {
	p := ctx.contextFields()
	p["authType"] = m.AuthType
	switch m.Name {
	case "radius":
		p["username"] = cfg.Username
		p["password"] = cfg.Password
	case "local":
		p["localuser"] = cfg.Username
		p["localuserPsw"] = cfg.Password
	case "simple":
		p["simplePassword"] = cfg.Password
	}
	return p
}

func maskedPayload(p map[string]any) string {
	copy := make(map[string]any, len(p))
	for k, v := range p {
		copy[k] = v
	}
	for _, k := range []string{"password", "localuserPsw", "simplePassword"} {
		if _, ok := copy[k]; ok {
			copy[k] = "***"
		}
	}
	data, _ := json.Marshal(copy)
	return string(data)
}

type loginResult struct {
	HTTPStatus int
	ErrorCode  *int
	Message    string
	Raw        string
}

// doLogin performs one portal authentication attempt.
func doLogin(cfg *Config, ctx *PortalContext, dryRun bool, log *Logger) error {
	if err := ctx.validate(cfg.PortalHosts); err != nil {
		return err
	}
	client := newPortalClient(cfg.timeout())
	defer client.CloseIdleConnections()

	settingURL := ctx.Origin + "/portal/getPortalPageSetting"
	status, body, err := postJSON(client, settingURL, ctx, ctx.contextFields())
	if err != nil {
		return fmt.Errorf("getPortalPageSetting: %w", err)
	}
	var setting portalSetting
	if err := json.Unmarshal(body, &setting); err != nil {
		return fmt.Errorf("getPortalPageSetting returned HTTP %d and invalid JSON: %w", status, err)
	}
	if setting.ErrorCode != nil && int(*setting.ErrorCode) != 0 {
		return fmt.Errorf("getPortalPageSetting: errorCode %d: %s", int(*setting.ErrorCode), setting.Msg)
	}
	var result settingResult
	if len(setting.Result) > 0 {
		if err := json.Unmarshal(setting.Result, &result); err != nil {
			return fmt.Errorf("parse portal setting: %w", err)
		}
	}
	method, err := resolveAuthMethod(result)
	if err != nil {
		return err
	}

	payload := method.payload(cfg, ctx)
	if dryRun {
		fmt.Printf("portal:     %s\n", ctx.PortalURL)
		fmt.Printf("origin:     %s\n", ctx.Origin)
		fmt.Printf("clientMac:  %s\n", ctx.ClientMAC)
		fmt.Printf("apMac:      %s\n", ctx.APMAC)
		fmt.Printf("gatewayMac: %s\n", ctx.GatewayMAC)
		fmt.Printf("ssid:       %s\n", ctx.SSID)
		fmt.Printf("radioId:    %s\n", ctx.RadioID)
		fmt.Printf("authType:   %d (%s)\n", method.AuthType, method.Name)
		fmt.Printf("endpoint:   %s%s\n", ctx.Origin, method.Endpoint)
		fmt.Printf("body:       %s\n", maskedPayload(payload))
		return nil
	}

	log.Printf("attempt: %s authType=%d client=%s ssid=%q ap=%s gw=%s",
		method.Name, method.AuthType, ctx.ClientMAC, ctx.SSID, ctx.APMAC, ctx.GatewayMAC)

	res, err := sendAuth(client, ctx, method, payload)
	if err != nil {
		log.Printf("login request failed: %v", err)
		return err
	}
	if res.ErrorCode != nil && *res.ErrorCode == 0 {
		if waitOnline(cfg, 3*time.Second) {
			log.Printf("login ok: %s @ %q (authType %d, client %s)", cfg.Username, ctx.SSID, method.AuthType, ctx.ClientMAC)
			return nil
		}
		msg := fmt.Sprintf("login accepted but internet not confirmed: %s @ %q (authType %d)", cfg.Username, ctx.SSID, method.AuthType)
		log.Printf("%s", msg)
		log.Printf("  if this repeats, the account may have hit its data/time quota; it will retry after the portal resets")
		return fmt.Errorf("%s", msg)
	}

	code := "?"
	if res.ErrorCode != nil {
		code = fmt.Sprint(*res.ErrorCode)
	}
	log.Printf("login failed: http=%d errorCode=%s msg=%q (authType %d, client %s)",
		res.HTTPStatus, code, res.Message, method.AuthType, ctx.ClientMAC)
	switch {
	case res.ErrorCode != nil && *res.ErrorCode == -41525:
		log.Printf("  -> portal says wrong password")
	case res.ErrorCode != nil && *res.ErrorCode == -41526:
		log.Printf("  -> portal says invalid client; toggle Wi-Fi off/on so a fresh portal URL is issued")
	case res.ErrorCode != nil && *res.ErrorCode == -41527:
		log.Printf("  -> portal says invalid local user")
	}
	return fmt.Errorf("login rejected: errorCode %s: %s", code, res.Message)
}

func sendAuth(client *http.Client, ctx *PortalContext, method authMethod, payload map[string]any) (loginResult, error) {
	status, body, err := postJSON(client, ctx.Origin+method.Endpoint, ctx, payload)
	if err != nil {
		return loginResult{}, err
	}
	res := loginResult{HTTPStatus: status, Raw: string(body)}
	var parsed struct {
		ErrorCode *flexInt `json:"errorCode"`
		Msg       string   `json:"msg"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return res, fmt.Errorf("auth returned HTTP %d and invalid JSON: %w", status, err)
	}
	res.Message = parsed.Msg
	if parsed.ErrorCode != nil {
		n := int(*parsed.ErrorCode)
		res.ErrorCode = &n
	}
	return res, nil
}

func postJSON(client *http.Client, endpoint string, ctx *PortalContext, payload map[string]any) (int, []byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", ctx.Origin)
	req.Header.Set("Referer", ctx.PortalURL)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; "+appName+")")

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func waitOnline(cfg *Config, total time.Duration) bool {
	deadline := time.Now().Add(total)
	for {
		res := probe(cfg, cfg.timeout())
		if res.Online {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(750 * time.Millisecond)
	}
}
