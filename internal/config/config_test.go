package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadCurrentConfig(t *testing.T) {
	t.Setenv("DATABASE_HOST", "127.0.0.1")
	t.Setenv("DATABASE_USER", "rsspod")
	t.Setenv("DATABASE_PASSWORD", "secret")
	t.Setenv("S3_ENDPOINT", "http://127.0.0.1:9000")
	t.Setenv("S3_ACCESS_KEY_ID", "access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("PUBLIC_MEDIA_BASE_URL", "http://127.0.0.1:9000/rsspod-media")
	t.Setenv("JINA_API_KEY", "")
	t.Setenv("JINA_PROXY", "")
	t.Setenv("CRAWL4AI_BASE_URL", "")
	t.Setenv("CRAWL4AI_API_TOKEN", "")
	t.Setenv("CRAWL4AI_PROXY", "")
	t.Setenv("LLM_DEEPSEEK_BASE_URL", "http://127.0.0.1:8080/v1")
	t.Setenv("LLM_DEEPSEEK_API_KEY", "secret")
	t.Setenv("LLM_DEEPSEEK_MODEL", "model-a")
	t.Setenv("LLM_DEEPSEEK_PROXY", "")
	t.Setenv("LLM_GEMINI_BASE_URL", "http://127.0.0.1:8081/v1")
	t.Setenv("LLM_GEMINI_API_KEY", "secret")
	t.Setenv("LLM_GEMINI_MODEL", "model-b")
	t.Setenv("LLM_GEMINI_PROXY", "")
	t.Setenv("EDGE_TTS_PROXY", "")
	t.Setenv("AZURE_SPEECH_REGION", "southeastasia")
	t.Setenv("AZURE_SPEECH_KEY", "speech-key")
	t.Setenv("AZURE_SPEECH_PROXY", "")

	path := filepath.Join("..", "..", "config.example.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Defaults.Content.Type != "rss-item" {
		t.Fatalf("default content type = %q", cfg.Defaults.Content.Type)
	}
	if got := cfg.Services.Content.Crawl4AI; got.EffectiveFormat() != "markdown" || got.EffectiveFilter() != "fit" {
		t.Fatalf("Crawl4AI service = %#v", got)
	}
	zhihu, ok := cfg.Source("zhihu-topic")
	if !ok || zhihu.Content == nil || zhihu.Content.Type != "derived-rss" {
		t.Fatalf("zhihu content was not decoded: %#v", zhihu.Content)
	}
	v2ex, ok := cfg.Source("v2ex-hot")
	if !ok {
		t.Fatal("v2ex source not found")
	}
	if got := cfg.EffectiveGeneration(v2ex); got.TargetDuration != "3m" || got.PromptTemplate == "" || got.DialogueProfile != "v2ex-commentary" {
		t.Fatalf("effective generation = %#v", got)
	}
	if got := cfg.DialogueProfiles[cfg.EffectiveGeneration(v2ex).DialogueProfile].Speakers[0]; got.Name != "小雅" || !strings.Contains(got.Role, "科技播客主持人") || got.Voice != "azure:zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural:xiaochen" {
		t.Fatalf("v2ex speaker = %#v", got)
	}
	if got := cfg.DialogueProfiles[cfg.EffectiveGeneration(zhihu).DialogueProfile].Speakers[0]; got.Name != "小雅" || !strings.Contains(got.Role, "中文互联网观察") {
		t.Fatalf("zhihu speaker = %#v", got)
	}
	if got := cfg.DialogueProfiles[cfg.Defaults.Generation.DialogueProfile]; cfg.Defaults.Generation.DialogueProfile != "general-dialogue" || !strings.HasPrefix(got.Speakers[0].Voice, "edge:") || !strings.Contains(got.Speakers[0].Role, "普通听众") {
		t.Fatalf("default dialogue profile = %#v", got)
	}
	if got := cfg.Services.TTS[EdgeTTSServiceName]; got.ConnectTimeout != "20s" || got.ReceiveTimeout != "120s" {
		t.Fatalf("edge TTS timeouts = %#v", got)
	}
	if got := cfg.Services.TTS[AzureTTSServiceName]; got.AzureEndpoint() != "https://southeastasia.tts.speech.microsoft.com/cognitiveservices/v1" || got.OutputFormat != "audio-24khz-48kbitrate-mono-mp3" {
		t.Fatalf("azure TTS service = %#v", got)
	}
	if got := cfg.EffectiveLimits(v2ex).MaxDocumentsPerItem; got != 300 {
		t.Fatalf("effective max documents = %d", got)
	}
	if got := cfg.EffectivePodcast(v2ex).MaxAge; got != "72h" {
		t.Fatalf("effective podcast max age = %q", got)
	}
}

func TestLoadDoesNotInterpolateEnvironmentIntoYAML(t *testing.T) {
	t.Setenv("TEST_PASSWORD", "colon: # still a scalar")
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := strings.ReplaceAll(minimalConfig, "PASSWORD", "env://TEST_PASSWORD")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Runtime.Database.Password != "colon: # still a scalar" {
		t.Fatalf("password = %q", cfg.Runtime.Database.Password)
	}
}

func TestHTTPManagementAddressDefaultsToLoopback(t *testing.T) {
	if got := (HTTPConfig{}).ManagementAddress(); got != "127.0.0.1:8081" {
		t.Fatalf("ManagementAddress() = %q, want 127.0.0.1:8081", got)
	}
}

func TestValidateLoopbackListen(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8081", "127.10.20.30:9000", "localhost:8081", "[::1]:8081"} {
		if err := validateLoopbackListen(address); err != nil {
			t.Errorf("validateLoopbackListen(%q) error = %v", address, err)
		}
	}
	for _, address := range []string{":8081", "0.0.0.0:8081", "192.168.1.2:8081", "[::]:8081", "localhost:0"} {
		if err := validateLoopbackListen(address); err == nil {
			t.Errorf("validateLoopbackListen(%q) unexpectedly succeeded", address)
		}
	}
}

func TestValidateOptionalProxy(t *testing.T) {
	for _, value := range []string{"", "http://127.0.0.1:4090", "socks5://localhost:1080"} {
		if err := validateOptionalProxy("proxy", value); err != nil {
			t.Errorf("validateOptionalProxy(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"localhost:4090", "://broken"} {
		if err := validateOptionalProxy("proxy", value); err == nil {
			t.Errorf("validateOptionalProxy(%q) unexpectedly succeeded", value)
		}
	}
}

func TestCrawl4AIServiceDefaults(t *testing.T) {
	service := Crawl4AIService{}
	if got := service.EffectiveFormat(); got != "markdown" {
		t.Fatalf("EffectiveFormat() = %q, want markdown", got)
	}
	if got := service.EffectiveFilter(); got != "fit" {
		t.Fatalf("EffectiveFilter() = %q, want fit", got)
	}
	if got, err := service.TimeoutDuration(); err != nil || got != 45*time.Second {
		t.Fatalf("TimeoutDuration() = %s, %v, want 45s", got, err)
	}
}

func TestValidateCrawl4AIContent(t *testing.T) {
	content := ContentConfig{Type: "crawl4ai", URL: URLMappingConfig{From: "item.link"}}
	service := Crawl4AIService{BaseURL: "http://crawl4ai:11235", Timeout: "45s", Format: "markdown"}
	if err := validateContent("test", content, ContentServices{Crawl4AI: service}); err != nil {
		t.Fatalf("valid Crawl4AI content rejected: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*Crawl4AIService)
		wantErr string
	}{
		{name: "missing base URL", mutate: func(s *Crawl4AIService) { s.BaseURL = "" }, wantErr: "not configured"},
		{name: "invalid base URL", mutate: func(s *Crawl4AIService) { s.BaseURL = "crawl4ai:11235" }, wantErr: "absolute URL"},
		{name: "invalid timeout", mutate: func(s *Crawl4AIService) { s.Timeout = "never" }, wantErr: "timeout"},
		{name: "unsupported format", mutate: func(s *Crawl4AIService) { s.Format = "html" }, wantErr: "format must be markdown"},
		{name: "unsupported filter", mutate: func(s *Crawl4AIService) { s.Filter = "bm25" }, wantErr: "filter must be raw or fit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := service
			test.mutate(&invalid)
			err := validateContent("test", content, ContentServices{Crawl4AI: invalid})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateContent() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestLoadRejectsUnknownTTSService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := strings.Replace(minimalConfig, "tts: {edge:", "tts: {chrome:", 1)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `unsupported service "chrome"`) {
		t.Fatalf("Load() error = %v, want unsupported chrome service", err)
	}
}

func TestLoadRejectsInvalidTTSTimeouts(t *testing.T) {
	for _, test := range []struct {
		field string
		old   string
		new   string
	}{
		{field: "connect_timeout", old: "connect_timeout: 1s", new: "connect_timeout: 0s"},
		{field: "receive_timeout", old: "receive_timeout: 2s", new: "receive_timeout: invalid"},
	} {
		t.Run(test.field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			data := strings.Replace(minimalConfig, test.old, test.new, 1)
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("Load() error = %v, want %s validation error", err, test.field)
			}
		})
	}
}

func TestValidateAzureTTSEndpoint(t *testing.T) {
	service := TTSService{
		Endpoint:       "https://southeastasia.tts.speech.microsoft.com/cognitiveservices/v1",
		APIKey:         "speech-key",
		OutputFormat:   "audio-24khz-48kbitrate-mono-mp3",
		ConnectTimeout: "1s",
		ReceiveTimeout: "2s",
	}
	cfg := Config{Services: ServicesConfig{TTS: map[string]TTSService{AzureTTSServiceName: service}}}
	if err := cfg.validateTTSServices(); err != nil {
		t.Fatalf("valid Azure endpoint rejected: %v", err)
	}
	service.Endpoint = "https://southeastasia.api.cognitive.microsoft.com/"
	cfg.Services.TTS[AzureTTSServiceName] = service
	if err := cfg.validateTTSServices(); err == nil || !strings.Contains(err.Error(), "/cognitiveservices/v1") {
		t.Fatalf("generic Cognitive Services endpoint error = %v", err)
	}
}

func TestParseSpeakerVoiceSplitsOnlyServicePrefix(t *testing.T) {
	voice, err := ParseSpeakerVoice("azure:zh-CN-Xiaoxiao2:DragonHDFlashLatestNeural")
	if err != nil {
		t.Fatal(err)
	}
	if voice.Service != "azure" || voice.Voice != "zh-CN-Xiaoxiao2:DragonHDFlashLatestNeural" || voice.Talker != "" {
		t.Fatalf("ParseSpeakerVoice() = %#v", voice)
	}
	for _, value := range []string{"", "edge", ":voice", "edge:"} {
		if _, err := ParseSpeakerVoice(value); err == nil {
			t.Errorf("ParseSpeakerVoice(%q) unexpectedly succeeded", value)
		}
	}
}

func TestParseSpeakerVoiceMultiTalker(t *testing.T) {
	voice, err := ParseSpeakerVoice("azure:zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural:xiaochen")
	if err != nil {
		t.Fatal(err)
	}
	if voice.Service != "azure" || voice.Voice != "zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural" || voice.Talker != "xiaochen" || !voice.IsMultiTalker() {
		t.Fatalf("ParseSpeakerVoice() = %#v", voice)
	}
	for _, value := range []string{
		"azure:zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural",
		"edge:zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural:xiaochen",
		"azure:zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural:bad talker",
	} {
		if _, err := ParseSpeakerVoice(value); err == nil {
			t.Errorf("ParseSpeakerVoice(%q) unexpectedly succeeded", value)
		}
	}
}

func TestValidateDialogueProfileAllowsMixedTTS(t *testing.T) {
	cfg := Config{Services: ServicesConfig{TTS: map[string]TTSService{
		EdgeTTSServiceName:  {},
		AzureTTSServiceName: {},
	}}}
	profile := DialogueProfile{
		Rate: "+0%", Volume: "+0%", Pitch: "+0Hz",
		Speakers: []SpeakerConfig{
			{ID: "host", Name: "Host", Role: "Host role", Voice: "edge:voice-a"},
			{ID: "guest", Name: "Guest", Role: "Guest role", Voice: "azure:voice:b"},
		},
	}
	if err := cfg.validateDialogueProfile("mixed", profile); err != nil {
		t.Fatalf("mixed TTS profile rejected: %v", err)
	}
}

func TestValidateDialogueProfileMultiTalker(t *testing.T) {
	cfg := Config{Services: ServicesConfig{TTS: map[string]TTSService{AzureTTSServiceName: {}}}}
	valid := DialogueProfile{
		Rate: "+0%", Volume: "+0%", Pitch: "+0Hz",
		Speakers: []SpeakerConfig{
			{ID: "host", Name: "Host", Role: "Host role", Voice: "azure:zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural:xiaochen"},
			{ID: "guest", Name: "Guest", Role: "Guest role", Voice: "azure:zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural:yunhan"},
		},
	}
	if err := cfg.validateDialogueProfile("multi", valid); err != nil {
		t.Fatalf("valid MultiTalker profile rejected: %v", err)
	}

	tests := []struct {
		name  string
		voice string
	}{
		{name: "mixed", voice: "azure:zh-CN-Xiaoxiao2:DragonHDFlashLatestNeural"},
		{name: "different-model", voice: "azure:en-US-MultiTalker-Ava-Andrew:DragonHDLatestNeural:andrew"},
		{name: "duplicate-talker", voice: "azure:zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural:xiaochen"},
		{name: "unknown-talker", voice: "azure:zh-CN-Multitalker-Xiaochen-Yunhan:DragonHDLatestNeural:other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := valid
			profile.Speakers = append([]SpeakerConfig(nil), valid.Speakers...)
			profile.Speakers[1].Voice = test.voice
			if err := cfg.validateDialogueProfile("multi", profile); err == nil {
				t.Fatal("invalid MultiTalker profile unexpectedly accepted")
			}
		})
	}
}

func TestRuntimeTimeoutDefaults(t *testing.T) {
	jobTimeout, err := (JobsConfig{}).TimeoutDuration()
	if err != nil {
		t.Fatal(err)
	}
	if jobTimeout != DefaultJobTimeout {
		t.Fatalf("default job timeout = %s, want %s", jobTimeout, DefaultJobTimeout)
	}
	jobFetchPollInterval, err := (JobsConfig{}).FetchPollIntervalDuration()
	if err != nil {
		t.Fatal(err)
	}
	if jobFetchPollInterval != DefaultJobFetchPollInterval {
		t.Fatalf("default job fetch poll interval = %s, want %s", jobFetchPollInterval, DefaultJobFetchPollInterval)
	}

	storageTimeout, err := (StorageConfig{}).TimeoutDuration()
	if err != nil {
		t.Fatal(err)
	}
	if storageTimeout != DefaultStorageOperationTimeout {
		t.Fatalf("default storage timeout = %s, want %s", storageTimeout, DefaultStorageOperationTimeout)
	}
}

func TestRuntimeTimeoutValidation(t *testing.T) {
	for _, value := range []string{"0s", "-1s", "invalid"} {
		if _, err := (JobsConfig{Timeout: value}).TimeoutDuration(); err == nil {
			t.Errorf("JobsConfig timeout %q unexpectedly succeeded", value)
		}
		if _, err := (JobsConfig{FetchPollInterval: value}).FetchPollIntervalDuration(); err == nil {
			t.Errorf("JobsConfig fetch poll interval %q unexpectedly succeeded", value)
		}
		if _, err := (StorageConfig{Timeout: value}).TimeoutDuration(); err == nil {
			t.Errorf("StorageConfig timeout %q unexpectedly succeeded", value)
		}
	}
	if _, err := (JobsConfig{FetchPollInterval: "50ms"}).FetchPollIntervalDuration(); err == nil {
		t.Error("JobsConfig fetch poll interval below River fetch cooldown unexpectedly succeeded")
	}
}

const minimalConfig = `
version: 6
runtime:
  http: {listen: ":8080"}
  database: {type: postgres, host: localhost, port: 5432, name: rsspod, user: app, password: PASSWORD, ssl_mode: disable}
  jobs:
    type: river
    queues: {source: {concurrency: 1}}
  storage:
    type: s3
    endpoint: http://localhost:9000
    region: us-east-1
    access_key: access
    secret_key: secret
    private_bucket: private
    media_bucket: media
    force_path_style: true
    public_media_base_url: http://localhost:9000/media
services:
  content: {jina: {base_url: https://r.jina.ai}}
  llm: {one: {type: openai_compatible, base_url: http://localhost:8000/v1, api_key: key, model: model, proxy: "", timeout: 1s}}
  tts: {edge: {proxy: "", connect_timeout: 1s, receive_timeout: 2s}}
dialogue_profiles:
  default:
    rate: "+0%"
    volume: "+0%"
    pitch: "+0Hz"
    speakers:
      - {id: host, name: Host, role: Friendly host, voice: edge:voice-a}
      - {id: guest, name: Guest, role: Critical guest, voice: edge:voice-b}
defaults:
  schedule: {timezone: Asia/Shanghai}
  llm: [one]
  generation:
    target_duration: 3m
    prompt_template: prompt.tmpl
    dialogue_profile: default
  content: {type: rss-item}
  limits: {max_feed_items_per_run: 10, max_documents_per_item: 10}
  podcast: {max_age: 72h}
sources:
  - id: test
    name: Test
    enabled: true
    feed: {url: http://localhost/feed.xml}
    schedule: {cron: "0 0 * * *"}
`
