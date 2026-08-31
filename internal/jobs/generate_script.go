package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/synrise25/rss-pod/internal/config"
)

type GenerateScriptWorker struct {
	river.WorkerDefaults[GenerateScriptArgs]
	Pool   *pgxpool.Pool
	Config *config.Config
	River  *river.Client[pgx.Tx]
}

type generatedScript struct {
	Title string          `json:"title"`
	Turns []generatedTurn `json:"turns"`
}

type generatedTurn struct {
	SpeakerID string `json:"speaker_id"`
	Text      string `json:"text"`
}

type llmDocument struct {
	Position  int
	Title     string
	SourceURL string
	Content   string
}

func (w *GenerateScriptWorker) Work(ctx context.Context, job *river.Job[GenerateScriptArgs]) error {
	if _, err := w.Pool.Exec(ctx, `
		UPDATE episodes SET status = 'generating_script', error = '', updated_at = now() WHERE id = $1
	`, job.Args.EpisodeID); err != nil {
		return fmt.Errorf("mark episode generating script: %w", err)
	}
	if err := w.generate(ctx, job.Args.EpisodeID); err != nil {
		return finishEpisodeAttempt(ctx, w.Pool, job.Args.EpisodeID, job.Attempt, job.MaxAttempts, err)
	}
	return nil
}

func (w *GenerateScriptWorker) generate(ctx context.Context, episodeID string) error {
	var sourceID, episodeTitle string
	if err := w.Pool.QueryRow(ctx, `SELECT source_id, title FROM episodes WHERE id = $1`, episodeID).Scan(&sourceID, &episodeTitle); err != nil {
		return fmt.Errorf("load episode: %w", err)
	}
	source, ok := w.Config.Source(sourceID)
	if !ok {
		return permanent("unknown source %q", sourceID)
	}
	documents, err := w.loadDocuments(ctx, episodeID)
	if err != nil {
		return err
	}
	generation := w.Config.EffectiveGeneration(source)
	speakers, err := w.loadSpeakers(ctx, episodeID)
	if err != nil {
		return err
	}
	systemPrompt, err := renderPrompt(generation.PromptTemplate, source, generation, speakers)
	if err != nil {
		return permanent("render prompt: %v", err)
	}
	userPrompt := renderDocuments(documents)

	var script generatedScript
	var raw []byte
	var usedService string
	var retryableErrors []string
	for _, name := range w.Config.EffectiveLLM(source) {
		service := w.Config.Services.LLM[name]
		response, responseRaw, retryable, err := callLLM(ctx, service, systemPrompt, userPrompt, speakers)
		if err == nil {
			if err := validateScript(response, speakers); err != nil {
				retryableErrors = append(retryableErrors, name+": returned unusable script: "+err.Error())
				continue
			}
			script, raw, usedService = response, responseRaw, name
			break
		}
		if !retryable {
			return permanent("LLM %s: %v", name, err)
		}
		retryableErrors = append(retryableErrors, name+": "+err.Error())
	}
	if usedService == "" {
		return fmt.Errorf("all LLM services failed to produce a usable script: %s", strings.Join(retryableErrors, "; "))
	}
	if script.Title == "" {
		script.Title = episodeTitle
	}

	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin script transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM script_turns WHERE episode_id = $1`, episodeID); err != nil {
		return fmt.Errorf("clear script turns: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO episode_scripts (episode_id, llm_service, title, raw_response)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (episode_id) DO UPDATE SET
		    llm_service = EXCLUDED.llm_service,
		    title = EXCLUDED.title,
		    raw_response = EXCLUDED.raw_response,
		    created_at = now()
	`, episodeID, usedService, script.Title, raw); err != nil {
		return fmt.Errorf("store script: %w", err)
	}
	for position, turn := range script.Turns {
		if _, err := tx.Exec(ctx, `
			INSERT INTO script_turns (episode_id, position, speaker_id, text)
			VALUES ($1, $2, $3, $4)
		`, episodeID, position, turn.SpeakerID, strings.TrimSpace(turn.Text)); err != nil {
			return fmt.Errorf("store script turn: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE episodes
		SET title = $2, status = 'script_ready', llm_service = $3, error = '', updated_at = now()
		WHERE id = $1
	`, episodeID, script.Title, usedService); err != nil {
		return fmt.Errorf("mark script ready: %w", err)
	}
	if _, err := w.River.InsertTx(ctx, tx, GenerateTTSArgs{EpisodeID: episodeID}, nil); err != nil {
		return fmt.Errorf("enqueue TTS generation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit script: %w", err)
	}
	return nil
}

func (w *GenerateScriptWorker) loadSpeakers(ctx context.Context, episodeID string) ([]config.SpeakerConfig, error) {
	rows, err := w.Pool.Query(ctx, `
		SELECT speaker_id, name, role, tts_service || ':' || voice
		FROM episode_speakers WHERE episode_id = $1 ORDER BY position
	`, episodeID)
	if err != nil {
		return nil, fmt.Errorf("query episode speakers: %w", err)
	}
	defer rows.Close()
	var speakers []config.SpeakerConfig
	for rows.Next() {
		var speaker config.SpeakerConfig
		if err := rows.Scan(&speaker.ID, &speaker.Name, &speaker.Role, &speaker.Voice); err != nil {
			return nil, fmt.Errorf("scan episode speaker: %w", err)
		}
		speakers = append(speakers, speaker)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(speakers) == 0 {
		return nil, permanent("episode has no speakers")
	}
	return speakers, nil
}

func (w *GenerateScriptWorker) loadDocuments(ctx context.Context, episodeID string) ([]llmDocument, error) {
	rows, err := w.Pool.Query(ctx, `
		SELECT position, title, source_url, content
		FROM documents WHERE episode_id = $1 ORDER BY position
	`, episodeID)
	if err != nil {
		return nil, fmt.Errorf("query documents: %w", err)
	}
	defer rows.Close()
	var documents []llmDocument
	for rows.Next() {
		var document llmDocument
		if err := rows.Scan(&document.Position, &document.Title, &document.SourceURL, &document.Content); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(documents) == 0 {
		return nil, permanent("episode has no documents")
	}
	return documents, nil
}

func renderPrompt(path string, source config.SourceConfig, generation config.GenerationConfig, speakers []config.SpeakerConfig) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	speakerLines := make([]string, 0, len(speakers))
	for _, speaker := range speakers {
		speakerLines = append(speakerLines, fmt.Sprintf("- %s（%s）：%s", speaker.ID, speaker.Name, speaker.Role))
	}
	replacer := strings.NewReplacer(
		"{{ source.name }}", source.Name,
		"{{ speaker_count }}", fmt.Sprintf("%d", len(speakers)),
		"{{ generation.target_duration }}", generation.TargetDuration,
		"{{ speakers }}", strings.Join(speakerLines, "\n"),
	)
	prompt := replacer.Replace(string(data))
	exampleSpeakerID := "speaker_id"
	if len(speakers) > 0 {
		exampleSpeakerID = speakers[0].ID
	}
	prompt += fmt.Sprintf(`

输出 JSON 的精确结构：
{"title":"本期标题","turns":[{"speaker_id":%q,"text":"台词"}]}
turns 必须按实际播放顺序排列。`, exampleSpeakerID)
	return prompt, nil
}

func renderDocuments(documents []llmDocument) string {
	const maxRunes = 120_000
	var builder strings.Builder
	for _, document := range documents {
		section := fmt.Sprintf("\n\n## 资料 %d\n标题：%s\n来源：%s\n\n%s", document.Position+1, document.Title, document.SourceURL, document.Content)
		remaining := maxRunes - utf8.RuneCountInString(builder.String())
		if remaining <= 0 {
			break
		}
		runes := []rune(section)
		if len(runes) > remaining {
			runes = runes[:remaining]
		}
		builder.WriteString(string(runes))
	}
	return strings.TrimSpace(builder.String())
}

func callLLM(ctx context.Context, service config.LLMService, systemPrompt, userPrompt string, speakers []config.SpeakerConfig) (generatedScript, []byte, bool, error) {
	timeout, err := time.ParseDuration(service.Timeout)
	if err != nil {
		return generatedScript{}, nil, false, fmt.Errorf("invalid timeout: %w", err)
	}
	requestBody := map[string]any{
		"model": service.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0.7,
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return generatedScript{}, nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(service.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return generatedScript{}, nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+service.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client, err := contentHTTPClient(service.Proxy, timeout)
	if err != nil {
		return generatedScript{}, nil, false, fmt.Errorf("invalid proxy: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return generatedScript{}, nil, true, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return generatedScript{}, nil, true, err
	}
	if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return generatedScript{}, nil, true, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return generatedScript{}, nil, false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(responseBody), 500))
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &completion); err != nil {
		return generatedScript{}, nil, true, fmt.Errorf("decode completion: %w", err)
	}
	if len(completion.Choices) == 0 {
		return generatedScript{}, nil, true, fmt.Errorf("completion contains no choices")
	}
	rawScript := extractJSONObject(completion.Choices[0].Message.Content)
	script, canonical, repairs, err := decodeGeneratedScript(rawScript, speakers)
	if err != nil {
		return generatedScript{}, nil, true, fmt.Errorf("decode script JSON: %w", err)
	}
	if len(repairs) > 0 {
		slog.InfoContext(ctx, "repaired LLM script JSON", "model", service.Model, "repairs", formatRepairs(repairs))
	}
	return script, canonical, false, nil
}

func validateScript(script generatedScript, speakers []config.SpeakerConfig) error {
	if len(script.Turns) == 0 {
		return fmt.Errorf("turns is empty")
	}
	allowed := make(map[string]struct{}, len(speakers))
	for _, speaker := range speakers {
		allowed[speaker.ID] = struct{}{}
	}
	for index, turn := range script.Turns {
		if _, ok := allowed[turn.SpeakerID]; !ok {
			return fmt.Errorf("turn %d uses unknown speaker %q", index, turn.SpeakerID)
		}
		if strings.TrimSpace(turn.Text) == "" {
			return fmt.Errorf("turn %d has empty text", index)
		}
	}
	return nil
}

func extractJSONObject(value string) []byte {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "```json")
	value = strings.TrimPrefix(value, "```")
	value = strings.TrimSuffix(value, "```")
	start := strings.IndexByte(value, '{')
	end := strings.LastIndexByte(value, '}')
	if start >= 0 && end >= start {
		value = value[start : end+1]
	}
	return []byte(strings.TrimSpace(value))
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
