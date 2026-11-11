package tui

import (
	"container/list"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

const renderRowCacheVersion = "render-row-v1"

const (
	defaultRenderCacheMaxEntries    = 256
	defaultRenderCacheMaxCharacters = 512 * 1024
)

var defaultRenderCache = newStaticRenderCache(defaultRenderCacheMaxEntries, defaultRenderCacheMaxCharacters)

type renderCacheStats struct {
	Hits             int
	Misses           int
	Evictions        int
	SkippedOversized int
}

type staticRenderCache struct {
	mu            sync.Mutex
	maxEntries    int
	maxCharacters int
	retained      int
	items         map[renderRowKey]*list.Element
	lru           *list.List
	statsData     renderCacheStats
}

type staticRenderCacheEntry struct {
	key   renderRowKey
	value string
	chars int
}

// renderRowKey is the exact cache identity of one rendered transcript row. Every
// field that the old concatenated string key carried lives here as its own
// typed field, so building the key copies only string headers and small values
// instead of the whole row text on every frame. String comparison never
// allocates, and struct equality is exact: two rows share a key only when every
// field matches, so the cache can never return another row's drawing.
type renderRowKey struct {
	version      string
	width        int
	flush        bool
	bodyCap      int
	optsExpanded bool
	cwd          string
	kind         rowKind
	id           string
	text         string
	metaAmount   string
	metaCurrency string
	tool         string
	status       tools.Status
	detail       string
	arg          string
	runID        int
	expanded     bool
	final        bool
	turnTools    int
	turnElapsed  time.Duration
	attachImages int
	attachDocs   int
	fileSelected bool
	rcHint       string
	rcArg        string
	rcAuto       bool
	permFP       string
	askFP        string
}

func newStaticRenderCache(maxEntries int, maxCharacters int) *staticRenderCache {
	return &staticRenderCache{
		maxEntries:    maxEntries,
		maxCharacters: maxCharacters,
		items:         map[renderRowKey]*list.Element{},
		lru:           list.New(),
	}
}

// clear drops every cached render. Called when the active theme changes so stale
// entries painted in the old palette are never reused.
func (c *staticRenderCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = map[renderRowKey]*list.Element{}
	c.lru.Init()
	c.retained = 0
	resetStreamingMarkdownCache()
}

func (c *staticRenderCache) render(key renderRowKey, stable bool, render func() string) string {
	if render == nil {
		return ""
	}
	if c == nil || !stable {
		return render()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		c.statsData.Hits++
		c.lru.MoveToFront(element)
		return element.Value.(*staticRenderCacheEntry).value
	}

	c.statsData.Misses++
	value := render()
	chars := utf8.RuneCountInString(value)
	if c.maxEntries <= 0 || c.maxCharacters <= 0 || chars > c.maxCharacters {
		c.statsData.SkippedOversized++
		return value
	}

	entry := &staticRenderCacheEntry{key: key, value: value, chars: chars}
	c.items[key] = c.lru.PushFront(entry)
	c.retained += chars
	c.evictOverflow()
	return value
}

func (c *staticRenderCache) evictOverflow() {
	for len(c.items) > c.maxEntries || c.retained > c.maxCharacters {
		element := c.lru.Back()
		if element == nil {
			c.retained = 0
			return
		}
		entry := element.Value.(*staticRenderCacheEntry)
		delete(c.items, entry.key)
		c.lru.Remove(element)
		c.retained -= entry.chars
		c.statsData.Evictions++
	}
}

func (m model) renderRowCacheKey(row transcriptRow, width int, rc rowContext, opts cardRenderOptions, flush bool) (renderRowKey, bool) {
	switch row.kind {
	case rowWelcome:
		return renderRowKey{}, false
	case rowToolCall:
		if m.pending && row.runID != 0 && row.runID == m.activeRunID {
			return renderRowKey{}, false
		}
	case rowPermission:
		event := row.permission
		if event != nil && event.ToolCallID != "" && event.Action == agent.PermissionActionPrompt &&
			m.pending && row.runID != 0 && row.runID == m.activeRunID {
			decided := false
			if len(rc.decided) > 0 {
				decided = rc.decided[rcKey(row.runID, event.ToolCallID)]
			}
			if !decided {
				return renderRowKey{}, false
			}
		}
	case rowSpecialist:
		if row.specialistInfo != nil && row.specialistInfo.status == specialistRunning {
			return renderRowKey{}, false
		}
	}

	var rcHint, rcArg string
	var rcAuto bool
	if row.id != "" && len(rc.hints)+len(rc.args)+len(rc.auto) > 0 {
		key := rcKey(row.runID, row.id)
		rcHint = rc.hints[key]
		rcArg = rc.args[key]
		rcAuto = rc.auto[key]
	}
	return renderRowKey{
		version:      renderRowCacheVersion,
		width:        width,
		flush:        flush,
		bodyCap:      opts.bodyCap,
		optsExpanded: opts.expanded,
		cwd:          opts.cwd,
		kind:         row.kind,
		id:           row.id,
		text:         row.text,
		metaAmount:   row.meta["amount"],
		metaCurrency: row.meta["currency"],
		tool:         row.tool,
		status:       row.status,
		detail:       row.detail,
		arg:          row.arg,
		runID:        row.runID,
		expanded:     row.expanded,
		final:        row.final,
		turnTools:    row.turnTools,
		turnElapsed:  row.turnElapsed,
		attachImages: row.attachments.images,
		attachDocs:   row.attachments.documents,
		// The FILES selection tints this row's card border, so selecting/deselecting
		// a file must miss the cache entry rendered under the other state.
		fileSelected: m.rowTouchesSelectedFile(row),
		rcHint:       rcHint,
		rcArg:        rcArg,
		rcAuto:       rcAuto,
		permFP:       permissionCacheFingerprint(row.permission),
		askFP:        askUserCacheFingerprint(row.askUser),
	}, true
}

func appendRenderCacheField(b *strings.Builder, value string) {
	b.WriteString(strconv.Itoa(len(value)))
	b.WriteByte(':')
	b.WriteString(value)
	b.WriteByte('|')
}

func permissionCacheFingerprint(event *agent.PermissionEvent) string {
	if event == nil {
		return ""
	}
	fields := []string{
		event.ToolCallID,
		event.ToolName,
		string(event.Action),
		string(event.DecisionAction),
		event.Permission,
		string(event.PermissionMode),
		event.Autonomy,
		event.SideEffect,
		event.Reason,
		event.Scope,
		string(event.Risk.Level),
		strconv.FormatBool(event.GrantMatched),
		strconv.FormatBool(event.Grant != nil),
		strconv.FormatBool(event.Grant != nil && event.Grant.Session),
	}
	if event.Block != nil {
		fields = append(fields,
			string(event.Block.Code),
			string(event.Block.Risk.Level),
			event.Block.Path,
			event.Block.Reason,
		)
	}
	var b strings.Builder
	for _, field := range fields {
		appendRenderCacheField(&b, field)
	}
	return b.String()
}

func askUserCacheFingerprint(request *agent.AskUserRequest) string {
	if request == nil {
		return ""
	}
	var b strings.Builder
	appendRenderCacheField(&b, request.ToolCallID)
	appendRenderCacheField(&b, request.Header)
	for _, question := range request.Questions {
		appendRenderCacheField(&b, question.Question)
		appendRenderCacheField(&b, strconv.FormatBool(question.MultiSelect))
		for _, option := range question.Options {
			appendRenderCacheField(&b, option)
		}
	}
	return b.String()
}
