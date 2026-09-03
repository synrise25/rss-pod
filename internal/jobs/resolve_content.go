package jobs

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mmcdole/gofeed"
	"github.com/riverqueue/river"
	htmlnode "golang.org/x/net/html"

	"github.com/synrise25/rss-pod/internal/config"
)

type ResolveContentWorker struct {
	river.WorkerDefaults[ResolveContentArgs]
	Pool   *pgxpool.Pool
	Config *config.Config
	River  *river.Client[pgx.Tx]
}

type resolvedDocument struct {
	Title     string
	SourceURL string
	Content   string
}

func (w *ResolveContentWorker) Work(ctx context.Context, job *river.Job[ResolveContentArgs]) error {
	if _, err := w.Pool.Exec(ctx, `
		UPDATE episodes SET status = 'resolving_content', error = '', updated_at = now() WHERE id = $1
	`, job.Args.EpisodeID); err != nil {
		return fmt.Errorf("mark episode resolving content: %w", err)
	}
	if err := w.resolve(ctx, job.Args.EpisodeID); err != nil {
		return finishEpisodeAttempt(ctx, w.Pool, job.Args.EpisodeID, job.Attempt, job.MaxAttempts, err)
	}
	return nil
}

func (w *ResolveContentWorker) resolve(ctx context.Context, episodeID string) error {
	var sourceID, title, link, description, itemContent string
	if err := w.Pool.QueryRow(ctx, `
		SELECT e.source_id, f.title, f.link, f.description, f.content
		FROM episodes e JOIN feed_items f ON f.id = e.feed_item_id
		WHERE e.id = $1
	`, episodeID).Scan(&sourceID, &title, &link, &description, &itemContent); err != nil {
		return fmt.Errorf("load episode input: %w", err)
	}
	source, ok := w.Config.Source(sourceID)
	if !ok {
		return permanent("unknown source %q", sourceID)
	}
	contentConfig := w.Config.EffectiveContent(source)
	var documents []resolvedDocument
	var err error
	switch contentConfig.Type {
	case "rss-item":
		content := itemContent
		if strings.TrimSpace(content) == "" {
			content = description
		}
		content = htmlToText(content)
		if strings.TrimSpace(content) == "" {
			return permanent("RSS item contains no usable content")
		}
		documents = []resolvedDocument{{Title: title, SourceURL: link, Content: content}}
	case "jina":
		var content string
		content, err = w.fetchJina(ctx, link, contentConfig)
		if err == nil {
			documents = []resolvedDocument{{Title: title, SourceURL: link, Content: content}}
		}
	case "crawl4ai":
		var content string
		content, err = w.fetchCrawl4AI(ctx, link, contentConfig)
		if err == nil {
			documents = []resolvedDocument{{Title: title, SourceURL: link, Content: content}}
		}
	case "derived-rss":
		documents, err = w.fetchDerivedRSS(ctx, source, contentConfig, link)
	default:
		return permanent("unsupported content type %q", contentConfig.Type)
	}
	if err != nil {
		return err
	}
	if len(documents) == 0 {
		return fmt.Errorf("content resolver returned no documents")
	}

	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin content transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM documents WHERE episode_id = $1`, episodeID); err != nil {
		return fmt.Errorf("clear documents: %w", err)
	}
	for position, document := range documents {
		if _, err := tx.Exec(ctx, `
			INSERT INTO documents (episode_id, position, title, source_url, content)
			VALUES ($1, $2, $3, $4, $5)
		`, episodeID, position, document.Title, document.SourceURL, document.Content); err != nil {
			return fmt.Errorf("store document: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE episodes SET status = 'content_ready', error = '', updated_at = now() WHERE id = $1
	`, episodeID); err != nil {
		return fmt.Errorf("mark content ready: %w", err)
	}
	if _, err := w.River.InsertTx(ctx, tx, GenerateScriptArgs{EpisodeID: episodeID}, nil); err != nil {
		return fmt.Errorf("enqueue script generation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit content: %w", err)
	}
	return nil
}

func (w *ResolveContentWorker) fetchJina(ctx context.Context, targetURL string, contentConfig config.ContentConfig) (string, error) {
	service := contentConfig.Jina.EffectiveService(w.Config.Services.Content.Jina)
	baseURL := strings.TrimSpace(service.BaseURL)
	if baseURL == "" {
		return "", permanent("Jina base_url is not configured")
	}
	timeout, err := service.TimeoutDuration()
	if err != nil {
		return "", permanent("invalid Jina timeout: %v", err)
	}
	client, err := contentHTTPClient(service.Proxy, timeout)
	if err != nil {
		return "", permanent("invalid Jina proxy: %v", err)
	}
	requestURL := strings.TrimRight(baseURL, "/") + "/" + targetURL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", permanent("create Jina request: %v", err)
	}
	if service.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+service.APIKey)
	}
	if service.Format != "" {
		req.Header.Set("X-Return-Format", service.Format)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Jina request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("read Jina response: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", fmt.Errorf("Jina returned HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", permanent("Jina returned HTTP %d", resp.StatusCode)
	}
	if strings.TrimSpace(string(body)) == "" {
		return "", fmt.Errorf("Jina returned empty content")
	}
	return string(body), nil
}

func (w *ResolveContentWorker) fetchDerivedRSS(ctx context.Context, source config.SourceConfig, contentConfig config.ContentConfig, itemLink string) ([]resolvedDocument, error) {
	if contentConfig.URL.From != "item.link" {
		return nil, permanent("derived-rss currently supports only url.from=item.link")
	}
	re, err := regexp.Compile(contentConfig.URL.Regex)
	if err != nil {
		return nil, permanent("compile derived-rss regex: %v", err)
	}
	match := re.FindStringSubmatch(itemLink)
	if match == nil {
		return nil, permanent("item link %q does not match derived-rss regex", itemLink)
	}
	values := make(map[string]string)
	for index, name := range re.SubexpNames() {
		if index > 0 && name != "" {
			values[name] = match[index]
		}
	}
	tmpl, err := template.New("derived-rss-url").Parse(contentConfig.URL.Template)
	if err != nil {
		return nil, permanent("parse derived-rss template: %v", err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, values); err != nil {
		return nil, permanent("render derived-rss URL: %v", err)
	}
	feedURL := rendered.String()
	client, err := contentHTTPClient("", 30*time.Second)
	if err != nil {
		return nil, err
	}
	parser := gofeed.NewParser()
	parser.Client = client
	feed, err := parser.ParseURLWithContext(feedURL, ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch derived RSS: %w", err)
	}
	limit := min(len(feed.Items), w.Config.EffectiveLimits(source).MaxDocumentsPerItem)
	documents := make([]resolvedDocument, 0, limit+1)
	for _, item := range feed.Items[:limit] {
		content := item.Content
		if strings.TrimSpace(content) == "" {
			content = item.Description
		}
		content = htmlToText(content)
		if strings.TrimSpace(content) == "" {
			continue
		}
		documents = append(documents, resolvedDocument{Title: item.Title, SourceURL: item.Link, Content: content})
	}
	if len(documents) > 0 {
		if channel, ok := derivedRSSChannelDocument(feed, feedURL); ok {
			documents = append([]resolvedDocument{channel}, documents...)
		}
	}
	return documents, nil
}

func derivedRSSChannelDocument(feed *gofeed.Feed, feedURL string) (resolvedDocument, bool) {
	title := htmlToText(feed.Title)
	description := htmlToText(feed.Description)
	if title == "" && description == "" {
		return resolvedDocument{}, false
	}

	lines := make([]string, 0, 2)
	if title != "" {
		lines = append(lines, "频道标题："+title)
	}
	if description != "" {
		lines = append(lines, "频道描述："+description)
	}
	sourceURL := strings.TrimSpace(feed.Link)
	if sourceURL == "" {
		sourceURL = feedURL
	}
	return resolvedDocument{
		Title:     "派生 RSS 频道信息",
		SourceURL: sourceURL,
		Content:   strings.Join(lines, "\n"),
	}, true
}

func contentHTTPClient(proxy string, timeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy = strings.TrimSpace(proxy)
	if proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
			return nil, fmt.Errorf("invalid proxy URL %q", proxy)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func htmlToText(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	root, err := htmlnode.Parse(strings.NewReader(input))
	if err != nil {
		return strings.TrimSpace(html.UnescapeString(input))
	}
	var builder strings.Builder
	var walk func(*htmlnode.Node, bool)
	walk = func(node *htmlnode.Node, skipped bool) {
		if node.Type == htmlnode.ElementNode && (node.Data == "script" || node.Data == "style") {
			skipped = true
		}
		if !skipped && node.Type == htmlnode.TextNode {
			text := strings.TrimSpace(node.Data)
			if text != "" {
				if builder.Len() > 0 {
					builder.WriteByte(' ')
				}
				builder.WriteString(text)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, skipped)
		}
		if !skipped && node.Type == htmlnode.ElementNode {
			switch node.Data {
			case "p", "div", "li", "br", "h1", "h2", "h3", "h4", "blockquote":
				builder.WriteByte('\n')
			}
		}
	}
	walk(root, false)
	lines := strings.FieldsFunc(builder.String(), func(r rune) bool { return r == '\n' || r == '\r' })
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
