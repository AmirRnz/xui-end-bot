package xui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xui-end-bot/internal/config"
)

type Client struct {
	baseURL             string
	subscriptionBaseURL string
	subscriptionPath    string
	apiToken            string
	httpClient          *http.Client
	Cache               *InboundCache
}

type WriteOutcome string

const (
	WriteSucceeded         WriteOutcome = "success"
	WriteDefinitiveFailure WriteOutcome = "definitive_failure"
	WriteUnknown           WriteOutcome = "unknown"
)

type WriteResult struct {
	Outcome WriteOutcome
	Err     error
}

type WriteError struct {
	Outcome WriteOutcome
	Err     error
}

// ErrNotFound is returned when the panel has confirmed that a client does
// not exist.  Callers must use IsNotFound instead of matching human-readable
// error strings because panel versions/locales vary their messages.
var ErrNotFound = errors.New("x-ui resource not found")

type NotFoundError struct {
	StatusCode int
	Message    string
}

func (e *NotFoundError) Error() string {
	if e.Message == "" {
		return ErrNotFound.Error()
	}
	return e.Message
}

func (e *NotFoundError) Unwrap() error { return ErrNotFound }

func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

func (e *WriteError) Error() string { return e.Err.Error() }
func (e *WriteError) Unwrap() error { return e.Err }

func IsUnknownOutcome(err error) bool {
	var writeErr *WriteError
	return errors.As(err, &writeErr) && writeErr.Outcome == WriteUnknown
}

func IsDefinitiveFailure(err error) bool {
	var writeErr *WriteError
	return errors.As(err, &writeErr) && writeErr.Outcome == WriteDefinitiveFailure
}

func NewClient(cfg *config.XUIConfig) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("x-ui config is nil")
	}

	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(cfg.URL, "/")
	}
	if baseURL == "" {
		return nil, fmt.Errorf("x-ui url is empty")
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	subscriptionBaseURL := normalizePublicURL(cfg.SubscriptionBaseURL)
	subscriptionPath := normalizeSubscriptionPath(cfg.SubscriptionPath)

	var tr *http.Transport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = t.Clone()
	} else {
		tr = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2: true,
		}
	}
	tr.MaxIdleConns = 100
	tr.MaxIdleConnsPerHost = 20

	return &Client{
		baseURL:             baseURL,
		subscriptionBaseURL: subscriptionBaseURL,
		subscriptionPath:    subscriptionPath,
		apiToken:            strings.TrimSpace(cfg.APIToken),
		httpClient: &http.Client{
			Timeout:   45 * time.Second,
			Jar:       jar,
			Transport: tr,
		},
	}, nil
}

func (c *Client) Login() error {
	if c.apiToken == "" {
		return fmt.Errorf("x-ui api_token is empty")
	}
	return nil
}

type PanelUpdateInfo struct {
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion"`
	UpdateAvailable bool   `json:"updateAvailable"`
}

type ReadinessStatus struct {
	MasterReachable bool   `json:"master_reachable"`
	TokenValid      bool   `json:"token_valid"`
	Version         string `json:"version"`
	VersionOK       bool   `json:"version_ok"`
	InboundsCount   int    `json:"inbounds_count"`
	Error           error  `json:"error,omitempty"`
}

func (c *Client) CheckReadiness(ctx context.Context) (*ReadinessStatus, error) {
	status := &ReadinessStatus{}
	if c == nil || c.apiToken == "" {
		status.Error = errors.New("API token is empty or client uninitialized")
		return status, status.Error
	}

	var updateInfo PanelUpdateInfo
	err := c.doRequest("GET", "/panel/api/server/getPanelUpdateInfo", nil, &updateInfo)
	if err != nil {
		status.Error = fmt.Errorf("master server unreachable or token invalid: %w", err)
		return status, status.Error
	}
	status.MasterReachable = true
	status.TokenValid = true
	status.Version = updateInfo.CurrentVersion
	normalizedVersion := strings.TrimPrefix(strings.TrimSpace(updateInfo.CurrentVersion), "v")
	status.VersionOK = strings.HasPrefix(normalizedVersion, "3.8.5")

	inbounds, err := c.GetInbounds()
	if err != nil {
		status.Error = fmt.Errorf("failed to fetch panel inbounds: %w", err)
		return status, status.Error
	}
	status.InboundsCount = len(inbounds)
	return status, nil
}

func (c *Client) GetInbounds() ([]Inbound, error) {
	var inbounds []Inbound
	if err := c.doRequest("GET", "/panel/api/inbounds/options", nil, &inbounds); err != nil {
		return nil, err
	}
	return inbounds, nil
}

func (c *Client) GetCachedInbounds() []Inbound {
	if c.Cache != nil {
		return c.Cache.GetAll()
	}
	return nil
}

func (c *Client) AddClient(req AddClientRequest) error {
	return c.AddClientResult(req).Err
}

func (c *Client) UpdateClient(email string, client ClientConfig) error {
	return c.UpdateClientResult(email, client).Err
}

func (c *Client) AddClientResult(req AddClientRequest) WriteResult {
	err := c.doRequest("POST", "/panel/api/clients/add", req, nil)
	if err == nil {
		return WriteResult{Outcome: WriteSucceeded}
	}
	if !isTimeoutError(err) {
		return WriteResult{Outcome: WriteDefinitiveFailure, Err: &WriteError{Outcome: WriteDefinitiveFailure, Err: err}}
	}
	// A timeout is ambiguous. Read the client back before deciding whether the
	// create committed; never issue a second non-idempotent create blindly.
	remote, verifyErr := c.GetClientByEmail(req.Client.Email)
	if verifyErr == nil && remote != nil && clientMatchesAddCore(*remote, req.Client) {
		missing := missingInboundIDs(remote.InboundIDs, req.InboundIDs)
		if len(missing) == 0 {
			return WriteResult{Outcome: WriteSucceeded}
		}

		// 3x-ui can commit the client while only attaching some inbounds before
		// the request times out. Attaching only the missing IDs is safe and keeps
		// the original create operation from being repeated.
		attachErr := c.AttachClient(req.Client.Email, missing)
		verified, readErr := c.GetClientByEmail(req.Client.Email)
		if readErr == nil && verified != nil && clientMatchesAddCore(*verified, req.Client) && len(missingInboundIDs(verified.InboundIDs, req.InboundIDs)) == 0 {
			return WriteResult{Outcome: WriteSucceeded}
		}
		if readErr != nil {
			verifyErr = readErr
		} else if attachErr != nil {
			verifyErr = fmt.Errorf("repairing missing inbound attachments: %w", attachErr)
		} else {
			verifyErr = fmt.Errorf("client %s is still missing requested inbound attachments", req.Client.Email)
		}
	}
	unknownErr := fmt.Errorf("x-ui add client outcome is unknown for %s: %w", req.Client.Email, err)
	if verifyErr != nil {
		unknownErr = fmt.Errorf("x-ui add client outcome is unknown for %s: %w (verification: %v)", req.Client.Email, err, verifyErr)
	}
	return WriteResult{Outcome: WriteUnknown, Err: &WriteError{Outcome: WriteUnknown, Err: unknownErr}}
}

func (c *Client) UpdateClientPatch(email string, patch ClientPatch) error {
	return c.UpdateClientPatchResult(email, patch).Err
}

func (c *Client) UpdateClientPatchResult(email string, patch ClientPatch) WriteResult {
	current, err := c.GetClientByEmail(email)
	if err != nil {
		outcome := WriteDefinitiveFailure
		if isTimeoutError(err) {
			outcome = WriteUnknown
		}
		return WriteResult{Outcome: outcome, Err: &WriteError{Outcome: outcome, Err: fmt.Errorf("cannot read current x-ui client %s: %w", email, err)}}
	}
	if current == nil {
		return WriteResult{Outcome: WriteDefinitiveFailure, Err: &WriteError{Outcome: WriteDefinitiveFailure, Err: fmt.Errorf("%w: client %s", ErrNotFound, email)}}
	}

	merged := mergeClientConfigWithPatch(*current, patch)
	endpoint := "/panel/api/clients/update/" + pathEscape(email)
	err = c.doRequest("POST", endpoint, merged, nil)
	if err == nil {
		return WriteResult{Outcome: WriteSucceeded}
	}
	if !isTimeoutError(err) {
		return WriteResult{Outcome: WriteDefinitiveFailure, Err: &WriteError{Outcome: WriteDefinitiveFailure, Err: err}}
	}

	// Timeout or network error! Verification step:
	remote, verifyErr := c.GetClientByEmail(email)
	if verifyErr == nil && remote != nil {
		matches := true
		if patch.Enable != nil && remote.Enable != *patch.Enable {
			matches = false
		}
		if patch.ExpiryTime != nil && remote.ExpiryTime != *patch.ExpiryTime {
			matches = false
		}
		if patch.LimitIP != nil && remote.LimitIP != *patch.LimitIP {
			matches = false
		}
		if patch.TotalGB != nil && remote.TotalGB != *patch.TotalGB {
			matches = false
		}

		if matches {
			return WriteResult{Outcome: WriteSucceeded}
		}
		return WriteResult{
			Outcome: WriteUnknown,
			Err:     &WriteError{Outcome: WriteUnknown, Err: fmt.Errorf("timeout verification failed: remote state does not reflect patched fields for %s: %w", email, err)},
		}
	}

	if verifyErr != nil {
		err = fmt.Errorf("%w (verification: %v)", err, verifyErr)
	}
	return WriteResult{Outcome: WriteUnknown, Err: &WriteError{Outcome: WriteUnknown, Err: fmt.Errorf("x-ui update client patch outcome is unknown for %s: %w", email, err)}}
}

func (c *Client) UpdateClientResult(email string, client ClientConfig) WriteResult {
	current, err := c.GetClientByEmail(email)
	if err != nil {
		outcome := WriteDefinitiveFailure
		if isTimeoutError(err) {
			outcome = WriteUnknown
		}
		return WriteResult{Outcome: outcome, Err: &WriteError{Outcome: outcome, Err: fmt.Errorf("cannot read current x-ui client %s: %w", email, err)}}
	}
	if current == nil {
		return WriteResult{Outcome: WriteDefinitiveFailure, Err: &WriteError{Outcome: WriteDefinitiveFailure, Err: fmt.Errorf("%w: client %s", ErrNotFound, email)}}
	}
	merged := mergeClientConfig(*current, client)
	endpoint := "/panel/api/clients/update/" + pathEscape(email)
	err = c.doRequest("POST", endpoint, merged, nil)
	if err == nil {
		return WriteResult{Outcome: WriteSucceeded}
	}
	if !isTimeoutError(err) {
		return WriteResult{Outcome: WriteDefinitiveFailure, Err: &WriteError{Outcome: WriteDefinitiveFailure, Err: err}}
	}
	remote, verifyErr := c.GetClientByEmail(email)
	if verifyErr == nil && remote != nil {
		if remote.Email == merged.Email && remote.Enable == merged.Enable && remote.ExpiryTime == merged.ExpiryTime && remote.LimitIP == merged.LimitIP && remote.TotalGB == merged.TotalGB {
			return WriteResult{Outcome: WriteSucceeded}
		}
		return WriteResult{
			Outcome: WriteUnknown,
			Err:     &WriteError{Outcome: WriteUnknown, Err: fmt.Errorf("timeout verification failed: remote state does not reflect patched fields for %s: %w", email, err)},
		}
	}
	if verifyErr != nil {
		err = fmt.Errorf("%w (verification: %v)", err, verifyErr)
	}
	return WriteResult{Outcome: WriteUnknown, Err: &WriteError{Outcome: WriteUnknown, Err: fmt.Errorf("x-ui update client outcome is unknown for %s: %w", email, err)}}
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout() || errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") || strings.Contains(strings.ToLower(err.Error()), "deadline exceeded")
}

func wrapWriteError(err error) error {
	if err == nil {
		return nil
	}
	outcome := WriteDefinitiveFailure
	if isTimeoutError(err) {
		outcome = WriteUnknown
	}
	return &WriteError{Outcome: outcome, Err: err}
}

func mergeClientConfig(current XUIClientInfo, desired ClientConfig) ClientConfig {
	currentID := current.UUID
	if currentID == "" && current.ID != 0 {
		currentID = strconv.Itoa(current.ID)
	}

	merged := ClientConfig{
		ID:                  currentID,
		Email:               current.Email,
		Enable:              desired.Enable,
		ExpiryTime:          desired.ExpiryTime,
		Flow:                current.Flow,
		Group:               current.Group,
		LimitIP:             desired.LimitIP,
		Reset:               current.Reset,
		ResetDay:            current.ResetDay,
		ResetMax:            current.ResetMax,
		Security:            current.Security,
		SubID:               current.SubID,
		TgID:                current.TgID,
		TotalGB:             current.TotalGB,
		Comment:             current.Comment,
		Password:            current.Password,
		Auth:                current.Auth,
		LimitHWID:           current.LimitHWID,
		KeepAlive:           current.KeepAlive,
		PrivateKey:          current.PrivateKey,
		PublicKey:           current.PublicKey,
		PreSharedKey:        current.PreSharedKey,
		AllowedIPs:          current.AllowedIPs,
		AllowedIPsByInbound: current.AllowedIPsByInbound,
		Secret:              current.Secret,
		AdTag:               current.AdTag,
		ForwardedPorts:      current.ForwardedPorts,
		TrafficReset:        current.TrafficReset,
		TrafficResetDay:     current.TrafficResetDay,
		Reverse:             current.Reverse,
	}

	if desired.ID != "" {
		merged.ID = desired.ID
	}
	if desired.Email != "" {
		merged.Email = desired.Email
	}
	if desired.SubID != "" {
		merged.SubID = desired.SubID
	}
	if desired.TgID != 0 {
		merged.TgID = desired.TgID
	}
	if desired.TotalGB != 0 {
		merged.TotalGB = desired.TotalGB
	}
	if desired.Flow != "" {
		merged.Flow = desired.Flow
	}
	if desired.LimitHWID != 0 {
		merged.LimitHWID = desired.LimitHWID
	}
	if desired.Group != "" {
		merged.Group = desired.Group
	}
	if desired.Comment != "" {
		merged.Comment = desired.Comment
	}
	if desired.Password != "" {
		merged.Password = desired.Password
	}
	if desired.Auth != "" {
		merged.Auth = desired.Auth
	}

	return merged
}

func mergeClientConfigWithPatch(current XUIClientInfo, patch ClientPatch) ClientConfig {
	currentID := current.UUID
	if currentID == "" && current.ID != 0 {
		currentID = strconv.Itoa(current.ID)
	}

	merged := ClientConfig{
		ID:                  currentID,
		Email:               current.Email,
		Enable:              current.Enable,
		ExpiryTime:          current.ExpiryTime,
		Flow:                current.Flow,
		Group:               current.Group,
		LimitIP:             current.LimitIP,
		Reset:               current.Reset,
		ResetDay:            current.ResetDay,
		ResetMax:            current.ResetMax,
		Security:            current.Security,
		SubID:               current.SubID,
		TgID:                current.TgID,
		TotalGB:             current.TotalGB,
		Comment:             current.Comment,
		Password:            current.Password,
		Auth:                current.Auth,
		LimitHWID:           current.LimitHWID,
		KeepAlive:           current.KeepAlive,
		PrivateKey:          current.PrivateKey,
		PublicKey:           current.PublicKey,
		PreSharedKey:        current.PreSharedKey,
		AllowedIPs:          current.AllowedIPs,
		AllowedIPsByInbound: current.AllowedIPsByInbound,
		Secret:              current.Secret,
		AdTag:               current.AdTag,
		ForwardedPorts:      current.ForwardedPorts,
		TrafficReset:        current.TrafficReset,
		TrafficResetDay:     current.TrafficResetDay,
		Reverse:             current.Reverse,
	}

	if patch.Enable != nil {
		merged.Enable = *patch.Enable
	}
	if patch.ExpiryTime != nil {
		merged.ExpiryTime = *patch.ExpiryTime
	}
	if patch.LimitIP != nil {
		merged.LimitIP = *patch.LimitIP
	}
	if patch.TotalGB != nil {
		merged.TotalGB = *patch.TotalGB
	}

	return merged
}

func clientMatchesAddCore(remote XUIClientInfo, desired ClientConfig) bool {
	identityMatches := desired.ID == ""
	if desired.ID != "" {
		if remote.UUID != "" {
			identityMatches = remote.UUID == desired.ID
		} else {
			// Some 3x-ui versions expose the client identity as password/auth
			// rather than uuid in the readback object.
			identityMatches = remote.Password == desired.ID || remote.Auth == desired.ID
		}
	}
	return identityMatches && remote.Email == desired.Email &&
		remote.SubID == desired.SubID &&
		remote.Enable == desired.Enable &&
		remote.ExpiryTime == desired.ExpiryTime &&
		remote.LimitIP == desired.LimitIP &&
		remote.TotalGB == desired.TotalGB
}

func missingInboundIDs(have, desired []int) []int {
	haveSet := make(map[int]struct{}, len(have))
	for _, id := range have {
		haveSet[id] = struct{}{}
	}
	missing := make([]int, 0, len(desired))
	seen := make(map[int]struct{}, len(desired))
	for _, id := range desired {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		if _, ok := haveSet[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

func (c *Client) DeleteClient(email string) error {
	return wrapWriteError(c.doRequest("POST", "/panel/api/clients/del/"+pathEscape(email)+"?keepTraffic=0", nil, nil))
}

func (c *Client) AttachClient(email string, inboundIDs []int) error {
	return wrapWriteError(c.doRequest("POST", "/panel/api/clients/"+pathEscape(email)+"/attach", attachRequest{InboundIDs: inboundIDs}, nil))
}

func (c *Client) GetSubscriptionLinks(subID string) ([]string, error) {
	var obj any
	err := c.doRequest("GET", "/panel/api/clients/subLinks/"+pathEscape(subID), nil, &obj)
	if err != nil {
		return nil, err
	}

	switch v := obj.(type) {
	case string:
		return c.publicSubscriptionLinks(subID, splitSubscriptionLinks(v)), nil
	case []any:
		links := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				links = append(links, splitSubscriptionLinks(s)...)
			}
		}
		return c.publicSubscriptionLinks(subID, links), nil
	case []string:
		return c.publicSubscriptionLinks(subID, v), nil
	default:
		return c.publicSubscriptionLinks(subID, nil), nil
	}
}

func (c *Client) GetClientTraffic(email string) (*ClientTraffic, error) {
	var traffic ClientTraffic
	if err := c.doRequest("GET", "/panel/api/clients/traffic/"+pathEscape(email), nil, &traffic); err != nil {
		return nil, err
	}
	return &traffic, nil
}

func (c *Client) publicSubscriptionLinks(subID string, links []string) []string {
	if c.subscriptionBaseURL == "" {
		return cleanSubscriptionLinks(links)
	}

	cleaned := cleanSubscriptionLinks(links)
	if len(cleaned) == 0 {
		return []string{c.SubscriptionURLFor(subID)}
	}

	out := make([]string, 0, len(cleaned))
	for _, link := range cleaned {
		out = append(out, c.publicSubscriptionLink(subID, link))
	}
	return out
}

func (c *Client) publicSubscriptionLink(subID, link string) string {
	parsed, err := url.Parse(link)
	if err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		base, baseErr := url.Parse(c.subscriptionBaseURL)
		if baseErr != nil || base.Host == "" {
			return link
		}
		if base.Path != "" && base.Path != "/" {
			return c.SubscriptionURLFor(subID)
		}
		parsed.Scheme = base.Scheme
		parsed.Host = base.Host
		return parsed.String()
	}

	if !strings.Contains(link, "://") {
		return c.SubscriptionURLFor(link)
	}
	return link
}

func (c *Client) SubscriptionURLFor(subID string) string {
	base, err := url.Parse(c.subscriptionBaseURL)
	if err != nil || base.Host == "" {
		return subID
	}

	prefix := base.Path
	if prefix == "" || prefix == "/" {
		prefix = c.subscriptionPath
	}
	base.Path = joinURLPath(prefix, subID)
	base.RawQuery = ""
	base.Fragment = ""
	return base.String()
}

func splitSubscriptionLinks(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	})
	links := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			links = append(links, field)
		}
	}
	return links
}

func cleanSubscriptionLinks(links []string) []string {
	if len(links) == 0 {
		return nil
	}
	out := make([]string, 0, len(links))
	seen := map[string]bool{}
	for _, link := range links {
		link = strings.TrimSpace(link)
		if link == "" || seen[link] {
			continue
		}
		seen[link] = true
		out = append(out, link)
	}
	return out
}

func normalizePublicURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	return strings.TrimRight(value, "/")
}

func normalizeSubscriptionPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/sub/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if !strings.HasSuffix(value, "/") {
		value += "/"
	}
	return value
}

func joinURLPath(prefix, subID string) string {
	prefix = normalizeSubscriptionPath(prefix)
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(subID, "/")
}

type BulkAttachRequest struct {
	Emails     []string `json:"emails"`
	InboundIDs []int    `json:"inboundIds"`
}

func (c *Client) BulkAttach(req BulkAttachRequest) error {
	return wrapWriteError(c.doRequest("POST", "/panel/api/clients/bulkAttach", req, nil))
}

func (c *Client) BulkDetach(req BulkAttachRequest) error {
	return wrapWriteError(c.doRequest("POST", "/panel/api/clients/bulkDetach", req, nil))
}

type BulkCreateItem struct {
	Client     ClientConfig `json:"client"`
	InboundIDs []int        `json:"inboundIds"`
}

type BulkCreateResponse struct {
	Created int                 `json:"created"`
	Skipped []BulkCreateSkipped `json:"skipped"`
}

type BulkCreateSkipped struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

func (c *Client) BulkCreate(req []BulkCreateItem) (*BulkCreateResponse, error) {
	var resp BulkCreateResponse
	if err := c.doRequest("POST", "/panel/api/clients/bulkCreate", req, &resp); err != nil {
		return nil, wrapWriteError(err)
	}
	return &resp, nil
}

func (c *Client) ListClients() ([]XUIClientInfo, error) {
	var clients []XUIClientInfo
	if err := c.doRequest("GET", "/panel/api/clients/list", nil, &clients); err != nil {
		return nil, err
	}
	return clients, nil
}

func (c *Client) GetClientByEmail(email string) (*XUIClientInfo, error) {
	var client XUIClientInfo
	if err := c.doRequest("GET", "/panel/api/clients/get/"+pathEscape(email), nil, &client); err != nil {
		return nil, err
	}
	return &client, nil
}

// FindClientBySubID searches for a client by subId using targeted paged search (/panel/api/clients/list/paged?search={subId}&pageSize=10&page={page}).
// It checks up to 3 pages (pageSize=10). If found, it fetches the full client details via GetClientByEmail.
// It never falls back to an unconstrained fleet scan.
func (c *Client) FindClientBySubID(subID string) (*XUIClientInfo, error) {
	subID = strings.TrimSpace(subID)
	if subID == "" {
		return nil, ErrNotFound
	}

	for page := 1; page <= 3; page++ {
		endpoint := fmt.Sprintf("/panel/api/clients/list/paged?search=%s&pageSize=10&page=%d", url.QueryEscape(subID), page)
		var pageResp struct {
			Filtered int             `json:"filtered"`
			Items    []XUIClientInfo `json:"items"`
		}
		err := c.doRequest("GET", endpoint, nil, &pageResp)
		if err != nil {
			if IsNotFound(err) {
				return nil, ErrNotFound
			}
			return nil, err
		}

		for _, item := range pageResp.Items {
			if item.SubID == subID {
				fullClient, fullErr := c.GetClientByEmail(item.Email)
				if fullErr == nil && fullClient != nil {
					return fullClient, nil
				}
				return &item, nil
			}
		}

		if len(pageResp.Items) == 0 || page*10 >= pageResp.Filtered {
			break
		}
	}

	return nil, ErrNotFound
}
