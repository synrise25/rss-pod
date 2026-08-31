package tts

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/synrise25/rss-pod/internal/config"
)

const maxAzureAudioBytes = 64 << 20

var makeAzureHTTPClient = azureHTTPClient

type ResponseError struct {
	StatusCode int
	Body       string
}

func (e *ResponseError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("Azure Speech returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("Azure Speech returned HTTP %d: %s", e.StatusCode, e.Body)
}

func (e *ResponseError) Retryable() bool {
	return e.StatusCode == http.StatusRequestTimeout || e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

type speak struct {
	XMLName xml.Name `xml:"speak"`
	Version string   `xml:"version,attr"`
	XMLNS   string   `xml:"xmlns,attr"`
	Lang    string   `xml:"xml:lang,attr"`
	Voice   voice    `xml:"voice"`
}

type voice struct {
	Name string `xml:"name,attr"`
	Text string `xml:",chardata"`
}

type multiTalkerSpeak struct {
	XMLName    xml.Name         `xml:"speak"`
	Version    string           `xml:"version,attr"`
	XMLNS      string           `xml:"xmlns,attr"`
	XMLNSMSTTS string           `xml:"xmlns:mstts,attr"`
	Lang       string           `xml:"xml:lang,attr"`
	Voice      multiTalkerVoice `xml:"voice"`
}

type multiTalkerVoice struct {
	Name   string            `xml:"name,attr"`
	Dialog multiTalkerDialog `xml:"mstts:dialog"`
}

type multiTalkerDialog struct {
	Turns []MultiTalkerTurn `xml:"mstts:turn"`
}

type MultiTalkerTurn struct {
	Speaker string `xml:"speaker,attr"`
	Text    string `xml:",chardata"`
}

func SynthesizeAzure(ctx context.Context, service config.TTSService, voiceName, text string) ([]byte, error) {
	body, err := xml.Marshal(speak{
		Version: "1.0",
		XMLNS:   "http://www.w3.org/2001/10/synthesis",
		Lang:    "zh-CN",
		Voice:   voice{Name: strings.TrimSpace(voiceName), Text: strings.TrimSpace(text)},
	})
	if err != nil {
		return nil, fmt.Errorf("encode Azure TTS SSML: %w", err)
	}
	return synthesizeAzureSSML(ctx, service, body)
}

func SynthesizeAzureMultiTalker(ctx context.Context, service config.TTSService, voiceName string, turns []MultiTalkerTurn) ([]byte, error) {
	if strings.TrimSpace(voiceName) == "" {
		return nil, fmt.Errorf("Azure MultiTalker voice must not be empty")
	}
	if len(turns) == 0 {
		return nil, fmt.Errorf("Azure MultiTalker requires at least one turn")
	}
	cleanTurns := make([]MultiTalkerTurn, len(turns))
	for i, turn := range turns {
		cleanTurns[i] = MultiTalkerTurn{
			Speaker: strings.TrimSpace(turn.Speaker),
			Text:    strings.TrimSpace(turn.Text),
		}
		if cleanTurns[i].Speaker == "" || cleanTurns[i].Text == "" {
			return nil, fmt.Errorf("Azure MultiTalker turn %d speaker and text must not be empty", i)
		}
	}
	body, err := xml.Marshal(multiTalkerSpeak{
		Version:    "1.0",
		XMLNS:      "http://www.w3.org/2001/10/synthesis",
		XMLNSMSTTS: "https://www.w3.org/2001/mstts",
		Lang:       "zh-CN",
		Voice: multiTalkerVoice{
			Name:   strings.TrimSpace(voiceName),
			Dialog: multiTalkerDialog{Turns: cleanTurns},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode Azure MultiTalker SSML: %w", err)
	}
	return synthesizeAzureSSML(ctx, service, body)
}

func synthesizeAzureSSML(ctx context.Context, service config.TTSService, body []byte) ([]byte, error) {
	connectTimeout, err := time.ParseDuration(service.ConnectTimeout)
	if err != nil || connectTimeout <= 0 {
		return nil, fmt.Errorf("invalid Azure TTS connect timeout %q", service.ConnectTimeout)
	}
	receiveTimeout, err := time.ParseDuration(service.ReceiveTimeout)
	if err != nil || receiveTimeout <= 0 {
		return nil, fmt.Errorf("invalid Azure TTS receive timeout %q", service.ReceiveTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, service.AzureEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Azure TTS request: %w", err)
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", service.APIKey)
	req.Header.Set("Content-Type", "application/ssml+xml")
	req.Header.Set("X-Microsoft-OutputFormat", service.OutputFormat)
	req.Header.Set("User-Agent", "rss-pod")

	client, err := makeAzureHTTPClient(service.Proxy, connectTimeout, receiveTimeout)
	if err != nil {
		return nil, fmt.Errorf("configure Azure TTS client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call Azure Speech: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxAzureAudioBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Azure Speech response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &ResponseError{StatusCode: resp.StatusCode, Body: truncate(strings.TrimSpace(string(responseBody)), 500)}
	}
	if len(responseBody) == 0 {
		return nil, fmt.Errorf("Azure Speech returned no audio")
	}
	if len(responseBody) > maxAzureAudioBytes {
		return nil, fmt.Errorf("Azure Speech audio exceeded %d bytes", maxAzureAudioBytes)
	}
	return responseBody, nil
}

func azureHTTPClient(proxy string, connectTimeout, receiveTimeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext
	if strings.TrimSpace(proxy) != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: transport, Timeout: receiveTimeout}, nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
