package checker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	edgetts "github.com/foresturquhart/edge-tts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/mmcdole/gofeed"

	"github.com/synrise25/rss-pod/internal/config"
	ttspkg "github.com/synrise25/rss-pod/internal/tts"
)

type Result struct {
	Name     string        `json:"name"`
	OK       bool          `json:"ok"`
	Detail   string        `json:"detail"`
	Duration time.Duration `json:"duration"`
}

func Run(ctx context.Context, cfg *config.Config) []Result {
	checks := []struct {
		name string
		fn   func(context.Context, *config.Config) (string, error)
	}{
		{name: "postgres", fn: checkPostgres},
		{name: "minio", fn: checkMinIO},
		{name: "public-media", fn: checkPublicMedia},
		{name: "rss", fn: checkRSS},
		{name: "jina", fn: checkJina},
		{name: "crawl4ai", fn: checkCrawl4AI},
		{name: "llm", fn: checkLLM},
		{name: "tts", fn: checkTTS},
	}

	results := make([]Result, 0, len(checks))
	for _, check := range checks {
		started := time.Now()
		checkCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
		detail, err := check.fn(checkCtx, cfg)
		cancel()
		result := Result{Name: check.name, OK: err == nil, Detail: detail, Duration: time.Since(started)}
		if err != nil {
			result.Detail = err.Error()
		}
		results = append(results, result)
	}
	return results
}

func AllOK(results []Result) bool {
	for _, result := range results {
		if !result.OK {
			return false
		}
	}
	return true
}

func checkPostgres(ctx context.Context, cfg *config.Config) (string, error) {
	pool, err := pgxpool.New(ctx, cfg.Runtime.Database.URL())
	if err != nil {
		return "", fmt.Errorf("create pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return "", fmt.Errorf("ping: %w", err)
	}

	var database, user string
	var superuser bool
	err = pool.QueryRow(ctx, `
		SELECT current_database(), current_user, rolsuper
		FROM pg_roles
		WHERE rolname = current_user
	`).Scan(&database, &user, &superuser)
	if err != nil {
		return "", fmt.Errorf("inspect role: %w", err)
	}
	if superuser {
		return "", fmt.Errorf("connected as %s, but application role must not be superuser", user)
	}
	return fmt.Sprintf("database=%s user=%s superuser=false", database, user), nil
}

func checkMinIO(ctx context.Context, cfg *config.Config) (string, error) {
	client, err := newMinIOClient(cfg.Runtime.Storage)
	if err != nil {
		return "", err
	}
	storage := cfg.Runtime.Storage
	buckets := []string{storage.PrivateBucket, storage.MediaBucket}
	for _, bucket := range buckets {
		exists, err := client.BucketExists(ctx, bucket)
		if err != nil {
			return "", fmt.Errorf("access bucket %s: %w", bucket, err)
		}
		if !exists {
			return "", fmt.Errorf("bucket %s does not exist", bucket)
		}
		name := "healthchecks/" + uuid.NewString() + ".txt"
		payload := []byte("ok")
		if _, err := client.PutObject(ctx, bucket, name, bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{ContentType: "text/plain"}); err != nil {
			return "", fmt.Errorf("write bucket %s: %w", bucket, err)
		}
		if err := client.RemoveObject(ctx, bucket, name, minio.RemoveObjectOptions{}); err != nil {
			return "", fmt.Errorf("remove healthcheck object from %s: %w", bucket, err)
		}
	}
	return strings.Join(buckets, ", ") + " read/write OK", nil
}

func checkPublicMedia(ctx context.Context, cfg *config.Config) (string, error) {
	storage := cfg.Runtime.Storage
	client, err := newMinIOClient(storage)
	if err != nil {
		return "", err
	}
	name := "healthchecks/" + uuid.NewString() + ".txt"
	payload := []byte("rss-pod public media check")
	if _, err := client.PutObject(ctx, storage.MediaBucket, name, bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{ContentType: "text/plain"}); err != nil {
		return "", fmt.Errorf("write public media test object: %w", err)
	}
	remove := func() error {
		return client.RemoveObject(ctx, storage.MediaBucket, name, minio.RemoveObjectOptions{})
	}

	publicURL := strings.TrimRight(storage.PublicMediaBaseURL, "/") + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicURL, nil)
	if err != nil {
		_ = remove()
		return "", err
	}
	resp, err := newHTTPClient("", 20*time.Second).Do(req)
	if err != nil {
		_ = remove()
		return "", fmt.Errorf("read public media URL: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
	expiration := resp.Header.Get("X-Amz-Expiration")
	resp.Body.Close()
	removeErr := remove()
	if removeErr != nil {
		return "", fmt.Errorf("remove public media test object: %w", removeErr)
	}
	if readErr != nil {
		return "", fmt.Errorf("read public media response: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("public media URL returned HTTP %d", resp.StatusCode)
	}
	if !bytes.Equal(body, payload) {
		return "", fmt.Errorf("public media response did not match uploaded object")
	}
	if expiration != "" {
		return "anonymous read OK; lifecycle " + expiration, nil
	}
	return "anonymous read OK", nil
}

func newMinIOClient(storage config.StorageConfig) (*minio.Client, error) {
	endpoint, err := url.Parse(storage.Endpoint)
	if err != nil {
		return nil, err
	}
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(storage.AccessKey, storage.SecretKey, ""),
		Secure:       endpoint.Scheme == "https",
		Region:       storage.Region,
		BucketLookup: minio.BucketLookupPath,
		Transport:    transportWithoutEnvironmentProxy(),
	})
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}
	return client, nil
}

func checkRSS(ctx context.Context, cfg *config.Config) (string, error) {
	parser := gofeed.NewParser()
	parser.Client = newHTTPClient("", 30*time.Second)
	checked := 0
	items := 0
	for _, source := range cfg.Sources {
		if !source.Enabled {
			continue
		}
		feed, err := parser.ParseURLWithContext(source.Feed.URL, ctx)
		if err != nil {
			return "", fmt.Errorf("source %s: %w", source.ID, err)
		}
		if len(feed.Items) == 0 {
			return "", fmt.Errorf("source %s returned no items", source.ID)
		}
		checked++
		items += len(feed.Items)
	}
	return fmt.Sprintf("%d enabled feeds, %d items", checked, items), nil
}

func checkJina(ctx context.Context, cfg *config.Config) (string, error) {
	services := make(map[config.JinaService]struct{})
	if cfg.Defaults.Content.Type == "jina" {
		services[cfg.Defaults.Content.Jina.EffectiveService(cfg.Services.Content.Jina)] = struct{}{}
	}
	for _, source := range cfg.Sources {
		if source.Content != nil && source.Content.Type == "jina" {
			services[source.Content.Jina.EffectiveService(cfg.Services.Content.Jina)] = struct{}{}
		}
	}
	if len(services) == 0 {
		return "not used", nil
	}
	var detail string
	for service := range services {
		var err error
		detail, err = checkJinaService(ctx, service)
		if err != nil {
			return "", err
		}
	}
	if len(services) > 1 {
		return fmt.Sprintf("%d configurations OK", len(services)), nil
	}
	return detail, nil
}

func checkJinaService(ctx context.Context, jina config.JinaService) (string, error) {
	baseURL := strings.TrimSpace(jina.BaseURL)
	if baseURL == "" {
		return "", fmt.Errorf("base_url is not configured")
	}
	timeout, err := jina.TimeoutDuration()
	if err != nil {
		return "", fmt.Errorf("timeout: %w", err)
	}
	requestURL := strings.TrimRight(baseURL, "/") + "/http://example.com"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", err
	}
	if jina.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+jina.APIKey)
	}
	client, err := newContentHTTPClient(jina.Proxy, timeout)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !bytes.Contains(bytes.ToLower(body), []byte("example domain")) {
		return "", fmt.Errorf("response did not contain expected content")
	}
	if strings.TrimSpace(jina.Proxy) != "" {
		return "content OK via configured proxy", nil
	}
	return "content OK via direct connection", nil
}

func checkCrawl4AI(ctx context.Context, cfg *config.Config) (string, error) {
	type crawlCheck struct {
		Service config.Crawl4AIService
		Mode    string
	}
	checks := make(map[crawlCheck]struct{})
	if cfg.Defaults.Content.Type == "crawl4ai" {
		service := cfg.Defaults.Content.Crawl4AI.EffectiveService(cfg.Services.Content.Crawl4AI)
		checks[crawlCheck{Service: service, Mode: service.EffectiveMode()}] = struct{}{}
	}
	for _, source := range cfg.Sources {
		if source.Content != nil && source.Content.Type == "crawl4ai" {
			service := source.Content.Crawl4AI.EffectiveService(cfg.Services.Content.Crawl4AI)
			checks[crawlCheck{Service: service, Mode: service.EffectiveMode()}] = struct{}{}
		}
	}
	if len(checks) == 0 {
		return "not used", nil
	}
	var detail string
	for check := range checks {
		var err error
		detail, err = checkCrawl4AIService(ctx, check.Service, check.Mode)
		if err != nil {
			return "", err
		}
	}
	if len(checks) > 1 {
		return fmt.Sprintf("%d configurations OK", len(checks)), nil
	}
	return detail, nil
}

func checkCrawl4AIService(ctx context.Context, service config.Crawl4AIService, mode string) (string, error) {
	baseURL := strings.TrimSpace(service.BaseURL)
	if baseURL == "" {
		return "", fmt.Errorf("base_url is not configured")
	}
	timeout, err := service.TimeoutDuration()
	if err != nil {
		return "", fmt.Errorf("timeout: %w", err)
	}
	var path string
	var payload []byte
	switch mode {
	case "crawl":
		path = "/crawl"
		payload, err = json.Marshal(struct {
			URLs []string `json:"urls"`
		}{URLs: []string{"https://example.com"}})
	case "md":
		path = "/md"
		payload, err = json.Marshal(struct {
			URL    string `json:"url"`
			Filter string `json:"f"`
		}{URL: "https://example.com", Filter: service.EffectiveFilter()})
	default:
		return "", fmt.Errorf("unsupported mode %q", mode)
	}
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if service.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+service.APIToken)
	}
	client, err := newContentHTTPClient(service.Proxy, timeout)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if mode == "crawl" {
		var result struct {
			Success bool `json:"success"`
			Results []struct {
				Success bool   `json:"success"`
				HTML    string `json:"html"`
			} `json:"results"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
			return "", fmt.Errorf("response: %w", err)
		}
		if !result.Success || len(result.Results) != 1 || !result.Results[0].Success || !strings.Contains(strings.ToLower(result.Results[0].HTML), "example domain") {
			return "", fmt.Errorf("response did not contain expected content")
		}
	} else {
		var result struct {
			Markdown string `json:"markdown"`
			Success  bool   `json:"success"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
			return "", fmt.Errorf("response: %w", err)
		}
		if !result.Success || !strings.Contains(strings.ToLower(result.Markdown), "example domain") {
			return "", fmt.Errorf("response did not contain expected content")
		}
	}
	content := service.EffectiveFilter() + " Markdown content"
	if mode == "crawl" {
		content = "HTML content"
	}
	if strings.TrimSpace(service.Proxy) != "" {
		return content + " OK via configured proxy", nil
	}
	return content + " OK via direct connection", nil
}

func checkLLM(ctx context.Context, cfg *config.Config) (string, error) {
	names := make([]string, 0, len(cfg.Services.LLM))
	for name := range cfg.Services.LLM {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		service := cfg.Services.LLM[name]
		timeout, err := time.ParseDuration(service.Timeout)
		if err != nil {
			return "", fmt.Errorf("%s timeout: %w", name, err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(service.BaseURL, "/")+"/models", nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+service.APIKey)
		resp, err := newHTTPClient(service.Proxy, timeout).Do(req)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		var payload struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%s models endpoint returned HTTP %d", name, resp.StatusCode)
		}
		if decodeErr != nil {
			return "", fmt.Errorf("%s models response: %w", name, decodeErr)
		}
		found := false
		for _, model := range payload.Data {
			if model.ID == service.Model {
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%s configured model %q not found", name, service.Model)
		}
	}
	return strings.Join(names, ", ") + " authentication and models OK", nil
}

func checkTTS(ctx context.Context, cfg *config.Config) (string, error) {
	profileNames := make([]string, 0, len(cfg.DialogueProfiles))
	for name := range cfg.DialogueProfiles {
		profileNames = append(profileNames, name)
	}
	slices.Sort(profileNames)
	for _, profileName := range profileNames {
		dialogue := cfg.DialogueProfiles[profileName]
		voices := make([]config.SpeakerVoice, len(dialogue.Speakers))
		for i, speaker := range dialogue.Speakers {
			voice, err := config.ParseSpeakerVoice(speaker.Voice)
			if err != nil {
				return "", fmt.Errorf("profile %s voice for %s: %w", profileName, speaker.ID, err)
			}
			voices[i] = voice
		}
		if voices[0].IsMultiTalker() {
			service := cfg.Services.TTS[voices[0].Service]
			turns := make([]ttspkg.MultiTalkerTurn, len(voices))
			for i, voice := range voices {
				turns[i] = ttspkg.MultiTalkerTurn{Speaker: voice.Talker, Text: dialogue.Speakers[i].Name + "，播客服务检查。"}
			}
			if _, err := ttspkg.SynthesizeAzureMultiTalker(ctx, service, voices[0].Voice, turns); err != nil {
				return "", fmt.Errorf("profile %s MultiTalker voice: %w", profileName, err)
			}
			continue
		}
		for i, speaker := range dialogue.Speakers {
			voice := voices[i]
			service := cfg.Services.TTS[voice.Service]
			check := func() error {
				switch voice.Service {
				case config.EdgeTTSServiceName:
					return checkEdgeTTSVoice(ctx, profileName, voice.Service, service, dialogue, speaker.ID, voice.Voice)
				case config.AzureTTSServiceName:
					return checkAzureTTSVoice(ctx, profileName, service, speaker.ID, voice.Voice)
				default:
					return fmt.Errorf("profile %s voice for %s uses unsupported TTS service %q", profileName, speaker.ID, voice.Service)
				}
			}
			var err error
			if voice.Service == config.EdgeTTSServiceName && service.Proxy == "" {
				err = withoutProxyEnvironment(check)
			} else {
				err = check()
			}
			if err != nil {
				return "", err
			}
		}
	}
	return strings.Join(profileNames, ", ") + " voices and synthesis OK", nil
}

func checkAzureTTSVoice(ctx context.Context, profileName string, service config.TTSService, speakerID, voiceName string) error {
	if _, err := ttspkg.SynthesizeAzure(ctx, service, voiceName, "播客服务检查。"); err != nil {
		return fmt.Errorf("profile %s voice for %s: %w", profileName, speakerID, err)
	}
	return nil
}

func checkEdgeTTSVoice(ctx context.Context, profileName, serviceName string, service config.TTSService, dialogue config.DialogueProfile, speakerID, voiceName string) error {
	voices, err := edgetts.ListVoices(ctx, service.Proxy)
	if err != nil {
		return fmt.Errorf("profile %s service %s list voices: %w", profileName, serviceName, err)
	}
	available := make(map[string]struct{}, len(voices))
	for _, voice := range voices {
		available[voice.ShortName] = struct{}{}
	}
	if _, ok := available[voiceName]; !ok {
		return fmt.Errorf("profile %s voice for %s not found: %s", profileName, speakerID, voiceName)
	}
	ttsConfig := edgetts.TTSConfig{
		Voice: voiceName, Rate: dialogue.Rate, Volume: dialogue.Volume, Pitch: dialogue.Pitch,
		Boundary: edgetts.SentenceBoundary,
	}
	connectTimeout, err := time.ParseDuration(service.ConnectTimeout)
	if err != nil {
		return fmt.Errorf("profile %s service %s connect timeout: %w", profileName, serviceName, err)
	}
	receiveTimeout, err := time.ParseDuration(service.ReceiveTimeout)
	if err != nil {
		return fmt.Errorf("profile %s service %s receive timeout: %w", profileName, serviceName, err)
	}
	options := []edgetts.CommunicateOption{
		edgetts.WithConnectTimeout(connectTimeout),
		edgetts.WithReceiveTimeout(receiveTimeout),
	}
	if service.Proxy != "" {
		options = append(options, edgetts.WithProxy(service.Proxy))
	}
	communicate, err := edgetts.NewCommunicate("播客服务检查。", ttsConfig, options...)
	if err != nil {
		return fmt.Errorf("profile %s: %w", profileName, err)
	}
	audioBytes := 0
	if err := communicate.Stream(ctx, func(chunk edgetts.TTSChunk) error {
		if chunk.Type == edgetts.ChunkTypeAudio {
			audioBytes += len(chunk.Data)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("profile %s synthesize: %w", profileName, err)
	}
	if audioBytes == 0 {
		return fmt.Errorf("profile %s returned no audio", profileName)
	}
	return nil
}

var proxyEnvironmentMu sync.Mutex

func withoutProxyEnvironment(fn func() error) error {
	proxyEnvironmentMu.Lock()
	defer proxyEnvironmentMu.Unlock()

	names := []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}
	type previousValue struct {
		value string
		set   bool
	}
	previous := make(map[string]previousValue, len(names))
	for _, name := range names {
		value, set := os.LookupEnv(name)
		previous[name] = previousValue{value: value, set: set}
		_ = os.Unsetenv(name)
	}
	defer func() {
		for _, name := range names {
			value := previous[name]
			if value.set {
				_ = os.Setenv(name, value.value)
			} else {
				_ = os.Unsetenv(name)
			}
		}
	}()
	return fn()
}

func newHTTPClient(proxy string, timeout time.Duration) *http.Client {
	transport := transportWithoutEnvironmentProxy()
	if proxy != "" {
		if proxyURL, err := url.Parse(proxy); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func newContentHTTPClient(proxy string, timeout time.Duration) (*http.Client, error) {
	proxy = strings.TrimSpace(proxy)
	if proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
			return nil, fmt.Errorf("invalid proxy URL %q", proxy)
		}
	}
	return newHTTPClient(proxy, timeout), nil
}

func transportWithoutEnvironmentProxy() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return transport
}
