package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

const CurrentVersion = 6

const (
	EdgeTTSServiceName  = "edge"
	AzureTTSServiceName = "azure"
)

const (
	DefaultJobTimeout              = 30 * time.Minute
	DefaultStorageOperationTimeout = 2 * time.Minute
)

type Config struct {
	Version          int                        `yaml:"version"`
	Runtime          RuntimeConfig              `yaml:"runtime"`
	Services         ServicesConfig             `yaml:"services"`
	DialogueProfiles map[string]DialogueProfile `yaml:"dialogue_profiles"`
	Defaults         DefaultsConfig             `yaml:"defaults"`
	Sources          []SourceConfig             `yaml:"sources"`
}

type RuntimeConfig struct {
	HTTP     HTTPConfig     `yaml:"http"`
	Database DatabaseConfig `yaml:"database"`
	Jobs     JobsConfig     `yaml:"jobs"`
	Storage  StorageConfig  `yaml:"storage"`
}

type HTTPConfig struct {
	Listen           string `yaml:"listen"`
	ManagementListen string `yaml:"management_listen"`
}

func (c HTTPConfig) ManagementAddress() string {
	if strings.TrimSpace(c.ManagementListen) == "" {
		return "127.0.0.1:8081"
	}
	return c.ManagementListen
}

type DatabaseConfig struct {
	Type     string `yaml:"type"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Name     string `yaml:"name"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	SSLMode  string `yaml:"ssl_mode"`
}

func (c DatabaseConfig) URL() string {
	u := &url.URL{
		Scheme: "postgres",
		Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:   c.Name,
		User:   url.UserPassword(c.User, c.Password),
	}
	q := u.Query()
	q.Set("sslmode", c.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

type JobsConfig struct {
	Type    string                 `yaml:"type"`
	Timeout string                 `yaml:"timeout"`
	Queues  map[string]QueueConfig `yaml:"queues"`
}

func (c JobsConfig) TimeoutDuration() (time.Duration, error) {
	return optionalDuration(c.Timeout, DefaultJobTimeout)
}

type QueueConfig struct {
	Concurrency int `yaml:"concurrency"`
}

type StorageConfig struct {
	Type               string `yaml:"type"`
	Endpoint           string `yaml:"endpoint"`
	Region             string `yaml:"region"`
	AccessKey          string `yaml:"access_key"`
	SecretKey          string `yaml:"secret_key"`
	PrivateBucket      string `yaml:"private_bucket"`
	MediaBucket        string `yaml:"media_bucket"`
	ForcePathStyle     bool   `yaml:"force_path_style"`
	PublicMediaBaseURL string `yaml:"public_media_base_url"`
	Timeout            string `yaml:"timeout"`
}

func (c StorageConfig) TimeoutDuration() (time.Duration, error) {
	return optionalDuration(c.Timeout, DefaultStorageOperationTimeout)
}

func optionalDuration(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, errors.New("must be a positive duration")
	}
	return duration, nil
}

type ServicesConfig struct {
	Content ContentServices       `yaml:"content"`
	LLM     map[string]LLMService `yaml:"llm"`
	TTS     map[string]TTSService `yaml:"tts"`
}

type ContentServices struct {
	Jina JinaService `yaml:"jina"`
}

type JinaService struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Proxy   string `yaml:"proxy"`
	Timeout string `yaml:"timeout"`
	Format  string `yaml:"format"`
}

type LLMService struct {
	Type    string `yaml:"type"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	Proxy   string `yaml:"proxy"`
	Timeout string `yaml:"timeout"`
}

type TTSService struct {
	Proxy          string `yaml:"proxy"`
	ConnectTimeout string `yaml:"connect_timeout"`
	ReceiveTimeout string `yaml:"receive_timeout"`
	Endpoint       string `yaml:"endpoint"`
	Region         string `yaml:"region"`
	APIKey         string `yaml:"api_key"`
	OutputFormat   string `yaml:"output_format"`
}

func (c TTSService) AzureEndpoint() string {
	if endpoint := strings.TrimSpace(c.Endpoint); endpoint != "" {
		return endpoint
	}
	return fmt.Sprintf("https://%s.tts.speech.microsoft.com/cognitiveservices/v1", strings.TrimSpace(c.Region))
}

type DialogueProfile struct {
	Rate     string          `yaml:"rate" json:"rate"`
	Volume   string          `yaml:"volume" json:"volume"`
	Pitch    string          `yaml:"pitch" json:"pitch"`
	Speakers []SpeakerConfig `yaml:"speakers" json:"speakers"`
}

type DefaultsConfig struct {
	Schedule   ScheduleConfig   `yaml:"schedule"`
	LLM        []string         `yaml:"llm"`
	Generation GenerationConfig `yaml:"generation"`
	Content    ContentConfig    `yaml:"content"`
	Limits     LimitsConfig     `yaml:"limits"`
	Podcast    PodcastConfig    `yaml:"podcast"`
}

type ScheduleConfig struct {
	Timezone string `yaml:"timezone"`
	Cron     string `yaml:"cron"`
}

type GenerationConfig struct {
	TargetDuration  string `yaml:"target_duration" json:"target_duration"`
	PromptTemplate  string `yaml:"prompt_template" json:"prompt_template"`
	DialogueProfile string `yaml:"dialogue_profile" json:"dialogue_profile"`
}

type SpeakerConfig struct {
	ID    string `yaml:"id" json:"id"`
	Name  string `yaml:"name" json:"name"`
	Role  string `yaml:"role" json:"role"`
	Voice string `yaml:"voice" json:"voice"`
}

type SpeakerVoice struct {
	Service string
	Voice   string
	Talker  string
}

func (v SpeakerVoice) IsMultiTalker() bool {
	return v.Talker != ""
}

func ParseSpeakerVoice(value string) (SpeakerVoice, error) {
	value = strings.TrimSpace(value)
	separator := strings.IndexByte(value, ':')
	if separator <= 0 || separator == len(value)-1 {
		return SpeakerVoice{}, errors.New("must use service:voice format")
	}
	result := SpeakerVoice{
		Service: strings.TrimSpace(value[:separator]),
		Voice:   strings.TrimSpace(value[separator+1:]),
	}
	if result.Service == "" || result.Voice == "" {
		return SpeakerVoice{}, errors.New("must use service:voice format")
	}
	if !isMultiTalkerVoice(result.Voice) {
		return result, nil
	}
	if result.Service != AzureTTSServiceName {
		return SpeakerVoice{}, errors.New("MultiTalker voices require the azure service")
	}
	if strings.Count(result.Voice, ":") < 2 {
		return SpeakerVoice{}, errors.New("MultiTalker voice must use service:voice:talker format")
	}
	talkerSeparator := strings.LastIndexByte(result.Voice, ':')
	if talkerSeparator <= 0 || talkerSeparator == len(result.Voice)-1 {
		return SpeakerVoice{}, errors.New("MultiTalker voice must use service:voice:talker format")
	}
	result.Talker = strings.TrimSpace(result.Voice[talkerSeparator+1:])
	result.Voice = strings.TrimSpace(result.Voice[:talkerSeparator])
	if result.Voice == "" || !validTalkerID.MatchString(result.Talker) {
		return SpeakerVoice{}, errors.New("MultiTalker voice contains an invalid talker")
	}
	return result, nil
}

var validTalkerID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

func isMultiTalkerVoice(voice string) bool {
	return strings.Contains(strings.ToLower(voice), "multitalker")
}

func multiTalkerNames(voice string) []string {
	lower := strings.ToLower(voice)
	marker := "multitalker-"
	start := strings.Index(lower, marker)
	if start < 0 {
		return nil
	}
	value := voice[start+len(marker):]
	if end := strings.IndexByte(value, ':'); end >= 0 {
		value = value[:end]
	}
	parts := strings.Split(value, "-")
	if len(parts) < 2 {
		return nil
	}
	return parts
}

type ContentConfig struct {
	Type string           `yaml:"type"`
	URL  URLMappingConfig `yaml:"url"`
}

type URLMappingConfig struct {
	From     string `yaml:"from"`
	Regex    string `yaml:"regex"`
	Template string `yaml:"template"`
}

type LimitsConfig struct {
	MaxFeedItemsPerRun  int `yaml:"max_feed_items_per_run"`
	MaxDocumentsPerItem int `yaml:"max_documents_per_item"`
}

type PodcastConfig struct {
	MaxAge string `yaml:"max_age" json:"max_age"`
}

type SourceConfig struct {
	ID         string            `yaml:"id" json:"id"`
	Name       string            `yaml:"name" json:"name"`
	Enabled    bool              `yaml:"enabled" json:"enabled"`
	Feed       FeedConfig        `yaml:"feed" json:"feed"`
	Schedule   ScheduleConfig    `yaml:"schedule" json:"schedule"`
	Content    *ContentConfig    `yaml:"content" json:"content,omitempty"`
	Generation *GenerationConfig `yaml:"generation" json:"generation,omitempty"`
	LLM        []string          `yaml:"llm" json:"llm,omitempty"`
	Limits     *LimitsConfig     `yaml:"limits" json:"limits,omitempty"`
	Podcast    *PodcastConfig    `yaml:"podcast" json:"podcast,omitempty"`
}

type FeedConfig struct {
	URL string `yaml:"url" json:"url"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := resolveEnvironment(&document); err != nil {
		return nil, err
	}

	var cfg Config
	if err := document.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func resolveEnvironment(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" && strings.HasPrefix(node.Value, "env://") {
		name := strings.TrimPrefix(node.Value, "env://")
		if name == "" {
			return errors.New("invalid empty env reference")
		}
		value, ok := os.LookupEnv(name)
		if !ok {
			return fmt.Errorf("environment variable %s is not set", name)
		}
		node.Value = value
	}
	for _, child := range node.Content {
		if err := resolveEnvironment(child); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d (expected %d)", c.Version, CurrentVersion)
	}
	if c.Runtime.HTTP.Listen == "" {
		return errors.New("runtime.http.listen must not be empty")
	}
	managementListen := c.Runtime.HTTP.ManagementAddress()
	if err := validateLoopbackListen(managementListen); err != nil {
		return fmt.Errorf("runtime.http.management_listen: %w", err)
	}
	if managementListen == c.Runtime.HTTP.Listen {
		return errors.New("runtime.http.listen and management_listen must be different")
	}
	if c.Runtime.Database.Type != "postgres" || c.Runtime.Database.Host == "" || c.Runtime.Database.Port <= 0 || c.Runtime.Database.Name == "" || c.Runtime.Database.User == "" {
		return errors.New("runtime.database must contain a valid postgres connection")
	}
	if c.Runtime.Jobs.Type != "river" {
		return errors.New("runtime.jobs.type must be river")
	}
	if _, err := c.Runtime.Jobs.TimeoutDuration(); err != nil {
		return fmt.Errorf("runtime.jobs.timeout %w", err)
	}
	for name, queue := range c.Runtime.Jobs.Queues {
		if queue.Concurrency < 1 {
			return fmt.Errorf("queue %q concurrency must be positive", name)
		}
	}
	if err := validateURL("runtime.storage.endpoint", c.Runtime.Storage.Endpoint); err != nil {
		return err
	}
	if err := validateURL("runtime.storage.public_media_base_url", c.Runtime.Storage.PublicMediaBaseURL); err != nil {
		return err
	}
	if c.Runtime.Storage.PrivateBucket == "" || c.Runtime.Storage.MediaBucket == "" {
		return errors.New("runtime.storage bucket names must not be empty")
	}
	if _, err := c.Runtime.Storage.TimeoutDuration(); err != nil {
		return fmt.Errorf("runtime.storage.timeout %w", err)
	}
	if _, err := time.LoadLocation(c.Defaults.Schedule.Timezone); err != nil {
		return fmt.Errorf("defaults.schedule.timezone: %w", err)
	}
	if err := c.validateTTSServices(); err != nil {
		return err
	}
	if len(c.DialogueProfiles) == 0 {
		return errors.New("dialogue_profiles must contain at least one profile")
	}
	for name, profile := range c.DialogueProfiles {
		if err := c.validateDialogueProfile(name, profile); err != nil {
			return err
		}
	}
	if err := c.validateGeneration("defaults.generation", c.Defaults.Generation); err != nil {
		return err
	}
	if c.Defaults.Limits.MaxFeedItemsPerRun < 1 || c.Defaults.Limits.MaxDocumentsPerItem < 1 {
		return errors.New("defaults limits must be positive")
	}
	if maxAge, err := time.ParseDuration(c.Defaults.Podcast.MaxAge); err != nil || maxAge <= 0 {
		return errors.New("defaults.podcast.max_age must be a positive duration")
	}
	if err := validateServiceReferences("defaults.llm", c.Defaults.LLM, c.Services.LLM); err != nil {
		return err
	}
	if err := validateOptionalProxy("services.content.jina.proxy", c.Services.Content.Jina.Proxy); err != nil {
		return err
	}
	for name, service := range c.Services.LLM {
		if err := validateOptionalProxy("services.llm "+name+" proxy", service.Proxy); err != nil {
			return err
		}
	}

	seen := make(map[string]struct{}, len(c.Sources))
	cronParser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	for i := range c.Sources {
		source := &c.Sources[i]
		if source.ID == "" || source.Name == "" {
			return fmt.Errorf("sources[%d] id and name must not be empty", i)
		}
		if _, ok := seen[source.ID]; ok {
			return fmt.Errorf("duplicate source id %q", source.ID)
		}
		seen[source.ID] = struct{}{}
		if _, err := cronParser.Parse(source.Schedule.Cron); err != nil {
			return fmt.Errorf("source %s schedule.cron: %w", source.ID, err)
		}
		if err := validateURL("source "+source.ID+" feed.url", source.Feed.URL); err != nil {
			return err
		}
		content := c.Defaults.Content
		if source.Content != nil {
			content = *source.Content
		}
		if err := validateContent(source.ID, content, c.Services.Content.Jina); err != nil {
			return err
		}
		if len(source.LLM) > 0 {
			if err := validateServiceReferences("source "+source.ID+" llm", source.LLM, c.Services.LLM); err != nil {
				return err
			}
		}
		if err := c.validateGeneration("source "+source.ID+" generation", c.EffectiveGeneration(*source)); err != nil {
			return err
		}
		if source.Podcast != nil {
			if maxAge, err := time.ParseDuration(c.EffectivePodcast(*source).MaxAge); err != nil || maxAge <= 0 {
				return fmt.Errorf("source %s podcast.max_age must be a positive duration", source.ID)
			}
		}
	}
	return nil
}

func (c *Config) validateTTSServices() error {
	if len(c.Services.TTS) == 0 {
		return errors.New("services.tts must contain at least one service")
	}
	for name, service := range c.Services.TTS {
		switch name {
		case EdgeTTSServiceName:
		case AzureTTSServiceName:
			if strings.TrimSpace(service.APIKey) == "" {
				return errors.New("services.tts azure api_key must not be empty")
			}
			if strings.TrimSpace(service.Endpoint) == "" && strings.TrimSpace(service.Region) == "" {
				return errors.New("services.tts azure region must not be empty when endpoint is not set")
			}
			if strings.TrimSpace(service.Endpoint) != "" {
				if err := validateURL("services.tts azure endpoint", service.Endpoint); err != nil {
					return err
				}
				endpoint, _ := url.Parse(service.Endpoint)
				if strings.TrimRight(endpoint.Path, "/") != "/cognitiveservices/v1" {
					return errors.New("services.tts azure endpoint must be the complete Speech synthesis endpoint ending in /cognitiveservices/v1")
				}
			}
			if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(service.OutputFormat)), "-mp3") {
				return errors.New("services.tts azure output_format must be an MP3 format")
			}
		default:
			return fmt.Errorf("services.tts contains unsupported service %q; supported services are %q and %q", name, EdgeTTSServiceName, AzureTTSServiceName)
		}
		if err := validateOptionalProxy("services.tts "+name+" proxy", service.Proxy); err != nil {
			return err
		}
		for _, timeoutConfig := range []struct {
			field string
			value string
		}{
			{field: "connect_timeout", value: service.ConnectTimeout},
			{field: "receive_timeout", value: service.ReceiveTimeout},
		} {
			timeout, err := time.ParseDuration(timeoutConfig.value)
			if err != nil || timeout <= 0 {
				return fmt.Errorf("services.tts %s %s must be a positive duration", name, timeoutConfig.field)
			}
		}
	}
	return nil
}

func (c *Config) validateGeneration(field string, generation GenerationConfig) error {
	if strings.TrimSpace(generation.TargetDuration) == "" {
		return fmt.Errorf("%s.target_duration must not be empty", field)
	}
	if strings.TrimSpace(generation.PromptTemplate) == "" {
		return fmt.Errorf("%s.prompt_template must not be empty", field)
	}
	if strings.TrimSpace(generation.DialogueProfile) == "" {
		return fmt.Errorf("%s.dialogue_profile must not be empty", field)
	}
	if _, ok := c.DialogueProfiles[generation.DialogueProfile]; !ok {
		return fmt.Errorf("%s references unknown dialogue profile %q", field, generation.DialogueProfile)
	}
	return nil
}

func (c *Config) validateDialogueProfile(name string, profile DialogueProfile) error {
	field := "dialogue_profiles." + name
	if strings.TrimSpace(name) == "" {
		return errors.New("dialogue profile name must not be empty")
	}
	if profile.Rate == "" || profile.Volume == "" || profile.Pitch == "" {
		return fmt.Errorf("%s rate, volume and pitch must not be empty", field)
	}
	if len(profile.Speakers) < 1 {
		return fmt.Errorf("%s.speakers must contain at least one speaker", field)
	}
	seen := make(map[string]struct{}, len(profile.Speakers))
	var voices []SpeakerVoice
	for i, speaker := range profile.Speakers {
		if strings.TrimSpace(speaker.ID) == "" || strings.TrimSpace(speaker.Name) == "" ||
			strings.TrimSpace(speaker.Role) == "" || strings.TrimSpace(speaker.Voice) == "" {
			return fmt.Errorf("%s.speakers[%d] id, name, role and voice must not be empty", field, i)
		}
		if _, ok := seen[speaker.ID]; ok {
			return fmt.Errorf("%s contains duplicate speaker id %q", field, speaker.ID)
		}
		voice, err := ParseSpeakerVoice(speaker.Voice)
		if err != nil {
			return fmt.Errorf("%s.speakers[%d].voice %w", field, i, err)
		}
		if _, ok := c.Services.TTS[voice.Service]; !ok {
			return fmt.Errorf("%s.speakers[%d].voice references unknown TTS service %q", field, i, voice.Service)
		}
		voices = append(voices, voice)
		seen[speaker.ID] = struct{}{}
	}
	if err := validateMultiTalkerProfile(voices); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	return nil
}

func validateMultiTalkerProfile(voices []SpeakerVoice) error {
	var model string
	talkers := make(map[string]struct{})
	multiTalkerCount := 0
	for _, voice := range voices {
		if !voice.IsMultiTalker() {
			continue
		}
		multiTalkerCount++
		if model == "" {
			model = voice.Voice
		} else if !strings.EqualFold(model, voice.Voice) {
			return errors.New("all speakers in a MultiTalker profile must use the same voice model")
		}
		talker := strings.ToLower(voice.Talker)
		if _, exists := talkers[talker]; exists {
			return fmt.Errorf("MultiTalker talker %q is assigned more than once", voice.Talker)
		}
		talkers[talker] = struct{}{}
	}
	if multiTalkerCount == 0 {
		return nil
	}
	if multiTalkerCount != len(voices) {
		return errors.New("MultiTalker and single-talker voices cannot be mixed in one profile")
	}
	if multiTalkerCount < 2 {
		return errors.New("a MultiTalker profile requires at least two speakers")
	}
	allowed := multiTalkerNames(model)
	if len(allowed) == 0 {
		return nil
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, talker := range allowed {
		allowedSet[strings.ToLower(talker)] = struct{}{}
	}
	for talker := range talkers {
		if _, ok := allowedSet[talker]; !ok {
			return fmt.Errorf("talker %q is not part of MultiTalker voice %q", talker, model)
		}
	}
	return nil
}

func validateOptionalProxy(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s must be empty or an absolute URL", field)
	}
	return nil
}

func validateLoopbackListen(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("must be a host:port address: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("must bind to a loopback address such as 127.0.0.1 or ::1")
	}
	return nil
}

func validateContent(sourceID string, content ContentConfig, jina JinaService) error {
	switch content.Type {
	case "rss-item":
		return nil
	case "jina":
		if jina.BaseURL == "" {
			return fmt.Errorf("source %s uses jina but services.content.jina is not configured", sourceID)
		}
	case "derived-rss":
		if content.URL.Regex == "" || content.URL.Template == "" {
			return fmt.Errorf("source %s derived-rss requires url.regex and url.template", sourceID)
		}
		if _, err := regexp.Compile(content.URL.Regex); err != nil {
			return fmt.Errorf("source %s content.url.regex: %w", sourceID, err)
		}
		if _, err := template.New("url").Parse(content.URL.Template); err != nil {
			return fmt.Errorf("source %s content.url.template: %w", sourceID, err)
		}
	default:
		return fmt.Errorf("source %s has unsupported content.type %q", sourceID, content.Type)
	}
	if content.URL.From == "" {
		return fmt.Errorf("source %s content.url.from must not be empty", sourceID)
	}
	return nil
}

func validateServiceReferences[T any](field string, names []string, services map[string]T) error {
	if len(names) == 0 {
		return fmt.Errorf("%s must contain at least one service", field)
	}
	for _, name := range names {
		if _, ok := services[name]; !ok {
			return fmt.Errorf("%s references unknown service %q", field, name)
		}
	}
	return nil
}

func validateURL(field, value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s must be an absolute URL", field)
	}
	return nil
}

func (c *Config) Source(id string) (SourceConfig, bool) {
	for _, source := range c.Sources {
		if source.ID == id {
			return source, true
		}
	}
	return SourceConfig{}, false
}

func (c *Config) EffectiveContent(source SourceConfig) ContentConfig {
	if source.Content != nil {
		return *source.Content
	}
	return c.Defaults.Content
}

func (c *Config) EffectiveGeneration(source SourceConfig) GenerationConfig {
	result := c.Defaults.Generation
	if source.Generation == nil {
		return result
	}
	if source.Generation.TargetDuration != "" {
		result.TargetDuration = source.Generation.TargetDuration
	}
	if source.Generation.PromptTemplate != "" {
		result.PromptTemplate = source.Generation.PromptTemplate
	}
	if source.Generation.DialogueProfile != "" {
		result.DialogueProfile = source.Generation.DialogueProfile
	}
	return result
}

func (c *Config) EffectiveLLM(source SourceConfig) []string {
	if len(source.LLM) > 0 {
		return source.LLM
	}
	return c.Defaults.LLM
}

func (c *Config) EffectiveLimits(source SourceConfig) LimitsConfig {
	result := c.Defaults.Limits
	if source.Limits == nil {
		return result
	}
	if source.Limits.MaxFeedItemsPerRun > 0 {
		result.MaxFeedItemsPerRun = source.Limits.MaxFeedItemsPerRun
	}
	if source.Limits.MaxDocumentsPerItem > 0 {
		result.MaxDocumentsPerItem = source.Limits.MaxDocumentsPerItem
	}
	return result
}

func (c *Config) EffectivePodcast(source SourceConfig) PodcastConfig {
	result := c.Defaults.Podcast
	if source.Podcast != nil && source.Podcast.MaxAge != "" {
		result.MaxAge = source.Podcast.MaxAge
	}
	return result
}
