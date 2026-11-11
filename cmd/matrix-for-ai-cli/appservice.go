package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var appserviceHTTP = &http.Client{Timeout: 60 * time.Second}

func putWebhook(base, token, webhookURL string) (secret string, err error) {
	body, _ := json.Marshal(map[string]string{"url": webhookURL})
	var out struct {
		Secret string `json:"secret"`
	}
	if err := appserviceCall(http.MethodPut, base+"/identities/me/webhook", token, body, &out); err != nil {
		return "", err
	}
	if out.Secret == "" {
		return "", fail(errAppserviceAnswer, "the appservice returned no secret")
	}
	return out.Secret, nil
}

func deleteWebhook(base, token string) error {
	return appserviceCall(http.MethodDelete, base+"/identities/me/webhook", token, nil, nil)
}

const appserviceAPIVersion = 1

// Each answer says the appservice's major (X-API-Version); serve also alerts on the desktop.
var otherAPIVersion = func(major string) {
	log.Printf("warning: the appservice speaks API v%s and this client v%d: update whichever is behind", major, appserviceAPIVersion)
}

var warnOtherAPIVersion sync.Once

type RoomUnread struct {
	Room       string      `json:"room"`
	Unread     int         `json:"unread"`
	LastUnread UnreadEvent `json:"last_unread"`
}

type UnreadEvent struct {
	Sender string `json:"sender"`
	TS     int64  `json:"ts"`
}

func fetchUnread(base, token string) (map[string]RoomUnread, error) {
	items, err := appservicePages(base+"/channels?unread=true", token, "channels")
	if err != nil {
		return nil, err
	}
	inbox := map[string]RoomUnread{}
	for _, it := range items {
		var r RoomUnread
		if json.Unmarshal(it, &r) == nil && r.Unread > 0 {
			inbox[r.Room] = r
		}
	}
	return inbox, nil
}

func appservicePages(u, token, key string) ([]json.RawMessage, error) {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	u += sep + "limit=200"
	var all []json.RawMessage
	for page := u; ; {
		var p map[string]json.RawMessage
		if err := appserviceCall(http.MethodGet, page, token, nil, &p); err != nil {
			return nil, err
		}
		var items []json.RawMessage
		if err := json.Unmarshal(p[key], &items); err != nil {
			return nil, fail(errAppserviceAnswer, "the appservice's answer to GET %s has no %s", requestPath(u), key)
		}
		all = append(all, items...)
		var cursor string
		json.Unmarshal(p["next_cursor"], &cursor)
		if cursor == "" {
			return all, nil
		}
		page = u + "&cursor=" + url.QueryEscape(cursor)
	}
}

func roomPath(base, room string) string { return base + channelPath(room) }

func appserviceCall(method, u, token string, body []byte, out any) error {
	data, err := appserviceRaw(method, u, token, body)
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fail(errAppserviceAnswer, "the appservice's answer to %s %s is not what this client expects: %v", method, requestPath(u), err)
	}
	return nil
}

// Media comes in base64: the cap is that large on purpose.
func appserviceRaw(method, u, token string, body []byte) ([]byte, error) {
	ctype := ""
	if body != nil {
		ctype = "application/json"
	}
	return appserviceSend(method, u, token, ctype, body)
}

func appserviceSend(method, u, token, ctype string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := appserviceHTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fail(errAppserviceUnreachable, "can't reach the appservice at %s: %v", req.URL.Host, err)
	}
	defer resp.Body.Close()
	if v := resp.Header.Get("X-API-Version"); v != "" && v != strconv.Itoa(appserviceAPIVersion) {
		warnOtherAPIVersion.Do(func() { otherAPIVersion(v) })
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, fail(errAppserviceUnreachable, "the appservice's answer was cut: %v", err)
	}
	if resp.StatusCode >= 300 {
		return nil, appserviceError(method, u, resp.StatusCode, data)
	}
	return data, nil
}

func requestPath(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Path
	}
	return u
}

// Only the codes an entity can fix by itself have a hint.
var hints = map[string]string{
	"signup_closed": "only the appservice's admin creates identities here: ask them",
	"invalid_room":  "a ROOM is a room ID, starting with ! (unread and channel list show them)",
	"unauthorized":  "the appservice does not take this identity's token: the appservice's admin can issue a new one (identity token MXID, with MATRIX_AI_ADMIN_TOKEN)",
	"not_member":    "you are not in that room (channel list shows yours)",
}

func appserviceError(method, u string, status int, data []byte) error {
	var p struct {
		Detail string `json:"detail"`
		Title  string `json:"title"`
		Code   string `json:"code"`
	}
	if json.Unmarshal(data, &p) != nil || p.Code == "" {
		answer := strings.TrimSpace(string(data))
		if len(answer) > 200 {
			answer = answer[:200] + "…"
		}
		return &AppserviceError{Status: status, msg: fmt.Sprintf("the appservice answered %d to %s %s: %s",
			status, method, requestPath(u), answer)}
	}
	detail := p.Detail
	if detail == "" {
		detail = p.Title
	}
	msg := fmt.Sprintf("%s (%s)", detail, p.Code)
	if h := hints[p.Code]; h != "" {
		msg += "\n" + h
	}
	return &AppserviceError{Status: status, Code: p.Code, Detail: detail, msg: msg}
}

func checkAppserviceBase(command, base string, insecure bool) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return usageError(command, "the appservice's URL %s is not http(s)", base)
	}
	if u.Scheme == "http" && !insecure && !isLoopback(u.Hostname()) {
		return usageError(command, "the appservice's URL %s is plain http, and the token would travel unencrypted: --insecure if that is what you want", base)
	}
	var caps struct {
		APIVersion int `json:"api_version"`
	}
	err = appserviceCall(http.MethodGet, base+"/capabilities", "", nil, &caps)
	if errors.Is(err, errAppserviceUnreachable) {
		return err
	}
	if err != nil || caps.APIVersion == 0 {
		return fail(errAppserviceAnswer, "no matrix-for-ai appservice answers at %s (GET /capabilities): the token was not sent", base)
	}
	return nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}
