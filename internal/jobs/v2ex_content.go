package jobs

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	htmlnode "golang.org/x/net/html"

	"github.com/synrise25/rss-pod/internal/config"
)

const (
	maxV2EXPages         = 100
	crawl4AIMaxBatchURLs = 10
)

var (
	v2exTopicPathPattern = regexp.MustCompile(`^/t/\d+/?$`)
	integerPattern       = regexp.MustCompile(`\d+`)
)

type v2exReply struct {
	ID      string
	Floor   int
	Author  string
	Content string
	Thanks  int
	Order   int
}

type v2exPage struct {
	Title     string
	Content   string
	PageCount int
	Replies   []v2exReply
}

func (w *ResolveContentWorker) fetchV2EXTopic(ctx context.Context, targetURL string, service config.Crawl4AIService) (string, error) {
	firstURL, topicURL, err := normalizeV2EXTopicURL(targetURL)
	if err != nil {
		return "", permanent("V2EX topic transform: %v", err)
	}
	results, err := w.fetchCrawl4AIResults(ctx, []string{firstURL}, service)
	if err != nil {
		return "", err
	}
	firstPage, err := parseV2EXPage(results[0].HTML, topicURL)
	if err != nil {
		return "", permanent("parse V2EX topic page 1: %v", err)
	}
	if firstPage.PageCount > maxV2EXPages {
		return "", permanent("V2EX topic reports %d pages, exceeding the safety limit of %d", firstPage.PageCount, maxV2EXPages)
	}

	pages := []v2exPage{firstPage}
	pageURLs := make([]string, 0, firstPage.PageCount-1)
	for page := 2; page <= firstPage.PageCount; page++ {
		pageURLs = append(pageURLs, v2exTopicPageURL(topicURL, page))
	}
	for start := 0; start < len(pageURLs); start += crawl4AIMaxBatchURLs {
		end := min(start+crawl4AIMaxBatchURLs, len(pageURLs))
		batchResults, err := w.fetchCrawl4AIResults(ctx, pageURLs[start:end], service)
		if err != nil {
			return "", err
		}
		for index, result := range batchResults {
			pageNumber := start + index + 2
			page, err := parseV2EXPage(result.HTML, topicURL)
			if err != nil {
				return "", permanent("parse V2EX topic page %d: %v", pageNumber, err)
			}
			pages = append(pages, page)
		}
	}

	return renderV2EXTopic(firstPage.Title, firstPage.Content, collectV2EXReplies(pages)), nil
}

func normalizeV2EXTopicURL(value string) (string, *url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", nil, fmt.Errorf("URL must be absolute")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", nil, fmt.Errorf("URL scheme must be http or https")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "v2ex.com" && !strings.HasSuffix(host, ".v2ex.com") {
		return "", nil, fmt.Errorf("URL host %q is not v2ex.com", parsed.Hostname())
	}
	if !v2exTopicPathPattern.MatchString(parsed.EscapedPath()) {
		return "", nil, fmt.Errorf("URL path %q is not a V2EX topic", parsed.EscapedPath())
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return v2exTopicPageURL(parsed, 1), parsed, nil
}

func v2exTopicPageURL(topicURL *url.URL, page int) string {
	pageURL := *topicURL
	query := pageURL.Query()
	query.Set("p", strconv.Itoa(page))
	pageURL.RawQuery = query.Encode()
	pageURL.Fragment = ""
	return pageURL.String()
}

func parseV2EXPage(document string, topicURL *url.URL) (v2exPage, error) {
	if strings.TrimSpace(document) == "" {
		return v2exPage{}, fmt.Errorf("page HTML is empty")
	}
	root, err := htmlnode.Parse(strings.NewReader(document))
	if err != nil {
		return v2exPage{}, err
	}
	page := v2exPage{PageCount: 1}
	walkHTML(root, func(node *htmlnode.Node) {
		if page.Title == "" && node.Type == htmlnode.ElementNode && node.Data == "h1" {
			page.Title = nodeText(node)
		}
		if page.Content == "" && hasClass(node, "topic_content") {
			page.Content = nodeText(node)
		}
		if node.Type == htmlnode.ElementNode && node.Data == "a" {
			if pageNumber := v2exPageNumber(node, topicURL.Path); pageNumber > page.PageCount {
				page.PageCount = pageNumber
			}
		}
		if node.Type == htmlnode.ElementNode && node.Data == "div" && hasClass(node, "cell") {
			if reply, ok := parseV2EXReply(node); ok {
				reply.Order = len(page.Replies)
				page.Replies = append(page.Replies, reply)
			}
		}
	})
	if page.Title == "" {
		return v2exPage{}, fmt.Errorf("topic title was not found")
	}
	return page, nil
}

func parseV2EXReply(node *htmlnode.Node) (v2exReply, bool) {
	id := attribute(node, "id")
	if !strings.HasPrefix(id, "r_") || len(id) == 2 {
		return v2exReply{}, false
	}
	reply := v2exReply{ID: strings.TrimPrefix(id, "r_")}
	walkHTML(node, func(descendant *htmlnode.Node) {
		if reply.Content == "" && hasClass(descendant, "reply_content") {
			reply.Content = nodeText(descendant)
		}
		if reply.Floor == 0 && hasClass(descendant, "no") {
			reply.Floor = firstInteger(nodeText(descendant))
		}
		if reply.Author == "" && descendant.Type == htmlnode.ElementNode && descendant.Data == "a" {
			href := attribute(descendant, "href")
			name := nodeText(descendant)
			if strings.HasPrefix(href, "/member/") && name != "" {
				reply.Author = name
			}
		}
		if reply.Thanks == 0 && isV2EXHeartImage(descendant) {
			reply.Thanks = integerAfterNode(descendant)
		}
	})
	if reply.Content == "" {
		return v2exReply{}, false
	}
	return reply, true
}

func collectV2EXReplies(pages []v2exPage) []v2exReply {
	seen := make(map[string]struct{})
	replies := make([]v2exReply, 0)
	order := 0
	for _, page := range pages {
		for _, reply := range page.Replies {
			if _, exists := seen[reply.ID]; exists {
				continue
			}
			seen[reply.ID] = struct{}{}
			reply.Order = order
			order++
			replies = append(replies, reply)
		}
	}
	sort.SliceStable(replies, func(i, j int) bool {
		if replies[i].Floor == 0 || replies[j].Floor == 0 {
			return replies[i].Order < replies[j].Order
		}
		return replies[i].Floor < replies[j].Floor
	})
	for index := range replies {
		if replies[index].Floor == 0 {
			replies[index].Floor = index + 1
		}
	}
	return replies
}

func renderV2EXTopic(title, content string, replies []v2exReply) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %s\n", strings.TrimSpace(title))
	if strings.TrimSpace(content) != "" {
		fmt.Fprintf(&builder, "\n## 原帖\n\n%s\n", strings.TrimSpace(content))
	}
	fmt.Fprintf(&builder, "\n## 回复（共 %d 条）\n", len(replies))
	for _, reply := range replies {
		author := strings.TrimSpace(reply.Author)
		if author == "" {
			author = "匿名"
		}
		fmt.Fprintf(&builder, "\n### #%d · %s", reply.Floor, author)
		if reply.Thanks > 0 {
			fmt.Fprintf(&builder, " · 感谢 %d", reply.Thanks)
		}
		fmt.Fprintf(&builder, "\n\n%s\n", strings.TrimSpace(reply.Content))
	}
	return strings.TrimSpace(builder.String())
}

func v2exPageNumber(node *htmlnode.Node, topicPath string) int {
	href := strings.TrimSpace(attribute(node, "href"))
	if href == "" {
		return 0
	}
	link, err := url.Parse(href)
	if err != nil || (link.Path != "" && strings.TrimRight(link.Path, "/") != strings.TrimRight(topicPath, "/")) {
		return 0
	}
	page, err := strconv.Atoi(link.Query().Get("p"))
	if err != nil || page < 1 {
		return 0
	}
	return page
}

func walkHTML(node *htmlnode.Node, visit func(*htmlnode.Node)) {
	visit(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}

func hasClass(node *htmlnode.Node, className string) bool {
	if node.Type != htmlnode.ElementNode {
		return false
	}
	for _, class := range strings.Fields(attribute(node, "class")) {
		if class == className {
			return true
		}
	}
	return false
}

func attribute(node *htmlnode.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}

func nodeText(node *htmlnode.Node) string {
	var builder strings.Builder
	var walk func(*htmlnode.Node, bool)
	walk = func(current *htmlnode.Node, skipped bool) {
		if current.Type == htmlnode.ElementNode && (current.Data == "script" || current.Data == "style") {
			skipped = true
		}
		if !skipped && current.Type == htmlnode.TextNode {
			text := strings.TrimSpace(current.Data)
			if text != "" {
				if builder.Len() > 0 {
					builder.WriteByte(' ')
				}
				builder.WriteString(text)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child, skipped)
		}
		if !skipped && current.Type == htmlnode.ElementNode {
			switch current.Data {
			case "p", "div", "li", "br", "pre", "blockquote":
				builder.WriteByte('\n')
			}
		}
	}
	walk(node, false)
	lines := strings.FieldsFunc(builder.String(), func(r rune) bool { return r == '\n' || r == '\r' })
	for index := range lines {
		lines[index] = strings.Join(strings.Fields(lines[index]), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func isV2EXHeartImage(node *htmlnode.Node) bool {
	if node.Type != htmlnode.ElementNode || node.Data != "img" {
		return false
	}
	src := strings.ToLower(attribute(node, "src"))
	alt := attribute(node, "alt")
	return strings.Contains(src, "heart_") || strings.Contains(src, "/heart.") || strings.Contains(alt, "❤")
}

func integerAfterNode(node *htmlnode.Node) int {
	var text strings.Builder
	for sibling := node.NextSibling; sibling != nil; sibling = sibling.NextSibling {
		text.WriteString(nodeText(sibling))
		text.WriteByte(' ')
	}
	return firstInteger(text.String())
}

func firstInteger(value string) int {
	match := integerPattern.FindString(value)
	if match == "" {
		return 0
	}
	result, _ := strconv.Atoi(match)
	return result
}
