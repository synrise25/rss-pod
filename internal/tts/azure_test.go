package tts

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/synrise25/rss-pod/internal/config"
)

func TestSynthesizeAzure(t *testing.T) {
	var requestBody []byte
	original := makeAzureHTTPClient
	t.Cleanup(func() { makeAzureHTTPClient = original })
	makeAzureHTTPClient = func(_ string, _, _ time.Duration) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s", r.Method)
			}
			if got := r.Header.Get("Ocp-Apim-Subscription-Key"); got != "speech-key" {
				t.Errorf("subscription key = %q", got)
			}
			if got := r.Header.Get("Content-Type"); got != "application/ssml+xml" {
				t.Errorf("content type = %q", got)
			}
			if got := r.Header.Get("X-Microsoft-OutputFormat"); got != "audio-24khz-48kbitrate-mono-mp3" {
				t.Errorf("output format = %q", got)
			}
			requestBody, _ = io.ReadAll(r.Body)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("mp3-data")), Header: make(http.Header)}, nil
		})}, nil
	}

	service := config.TTSService{
		Endpoint:       "https://speech.test/cognitiveservices/v1",
		APIKey:         "speech-key",
		OutputFormat:   "audio-24khz-48kbitrate-mono-mp3",
		ConnectTimeout: "1s",
		ReceiveTimeout: "2s",
	}
	audio, err := SynthesizeAzure(context.Background(), service, "zh-CN-Xiaoxiao2:DragonHDFlashLatestNeural", "甲 & <乙>")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(audio); got != "mp3-data" {
		t.Fatalf("audio = %q", got)
	}
	if !strings.Contains(string(requestBody), ` xml:lang="zh-CN"`) {
		t.Fatalf("SSML is missing xml:lang: %s", requestBody)
	}
	if strings.Contains(string(requestBody), `_xml`) || strings.Contains(string(requestBody), `xmlns:xml=`) {
		t.Fatalf("SSML contains an invalid xml namespace: %s", requestBody)
	}

	var document speak
	if err := xml.Unmarshal(requestBody, &document); err != nil {
		t.Fatalf("request is not valid SSML: %v\n%s", err, requestBody)
	}
	if document.Voice.Name != "zh-CN-Xiaoxiao2:DragonHDFlashLatestNeural" || document.Voice.Text != "甲 & <乙>" {
		t.Fatalf("SSML = %#v", document)
	}
}

func TestSynthesizeAzureMultiTalker(t *testing.T) {
	var requestBody []byte
	original := makeAzureHTTPClient
	t.Cleanup(func() { makeAzureHTTPClient = original })
	makeAzureHTTPClient = func(_ string, _, _ time.Duration) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requestBody, _ = io.ReadAll(r.Body)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("mp3-data")), Header: make(http.Header)}, nil
		})}, nil
	}

	service := config.TTSService{
		Endpoint:       "https://speech.test/cognitiveservices/v1",
		APIKey:         "speech-key",
		OutputFormat:   "audio-24khz-48kbitrate-mono-mp3",
		ConnectTimeout: "1s",
		ReceiveTimeout: "2s",
	}
	audio, err := SynthesizeAzureMultiTalker(context.Background(), service,
		"zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural",
		[]MultiTalkerTurn{
			{Speaker: "xiaochen", Text: "甲 & <乙>"},
			{Speaker: "yunhan", Text: "丙"},
		})
	if err != nil {
		t.Fatal(err)
	}
	if string(audio) != "mp3-data" {
		t.Fatalf("audio = %q", audio)
	}
	ssml := string(requestBody)
	for _, want := range []string{
		`xmlns:mstts="https://www.w3.org/2001/mstts"`,
		`xml:lang="zh-CN"`,
		`<voice name="zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural">`,
		`<mstts:dialog>`,
		`<mstts:turn speaker="xiaochen">甲 &amp; &lt;乙&gt;</mstts:turn>`,
		`<mstts:turn speaker="yunhan">丙</mstts:turn>`,
	} {
		if !strings.Contains(ssml, want) {
			t.Errorf("SSML is missing %q: %s", want, ssml)
		}
	}
}

func TestSynthesizeAzureClassifiesHTTPError(t *testing.T) {
	original := makeAzureHTTPClient
	t.Cleanup(func() { makeAzureHTTPClient = original })
	makeAzureHTTPClient = func(_ string, _, _ time.Duration) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader("quota exceeded")), Header: make(http.Header)}, nil
		})}, nil
	}

	_, err := SynthesizeAzure(context.Background(), config.TTSService{
		Endpoint:       "https://speech.test/cognitiveservices/v1",
		APIKey:         "speech-key",
		OutputFormat:   "audio-24khz-48kbitrate-mono-mp3",
		ConnectTimeout: "1s",
		ReceiveTimeout: "2s",
	}, "voice", "text")
	responseErr, ok := err.(*ResponseError)
	if !ok || !responseErr.Retryable() {
		t.Fatalf("error = %#v, want retryable ResponseError", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
