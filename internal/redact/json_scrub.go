package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

const defaultAnalyzeConcurrency = 4

type jsonScrubTask struct {
	text    string
	setText func(string)
}

func (r *Redactor) scrubJSON(ctx context.Context, text string, reg *Registry, forceRedactMarkers bool) (Result, error) {
	var root any
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		return r.scrub(ctx, text, reg, forceRedactMarkers)
	}

	// Bare JSON scalars (a quoted string, number, bool, null) are valid JSON
	// but the container walk below produces zero tasks for them, which would
	// return the text without ever analyzing it. Treat them as plain text.
	switch root.(type) {
	case map[string]any, []any:
	default:
		return r.scrub(ctx, text, reg, forceRedactMarkers)
	}

	adapter := AdapterForContext(ctx)
	var tasks []jsonScrubTask
	collectJSONScrubTasks(root, nil, &tasks, adapter)

	ctx = r.prefetchAnalyzeCache(ctx, tasks)

	acc := Result{EntityCounts: map[string]int{}}
	if err := r.runJSONScrubTasks(ctx, tasks, reg, forceRedactMarkers, &acc); err != nil {
		return Result{}, err
	}

	out, err := marshalJSONPreserveHTML(root)
	if err != nil {
		return Result{}, fmt.Errorf("redact: marshal scrubbed JSON: %w", err)
	}
	acc.Text = string(out)
	return acc, nil
}

func collectJSONScrubTasks(v any, path []string, tasks *[]jsonScrubTask, adapter ContentAdapter) {
	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			childPath := append(path, k)
			if s, ok := child.(string); ok && adapter.ScrubString(path, k) {
				key := k
				*tasks = append(*tasks, jsonScrubTask{
					text: s,
					setText: func(scrubbed string) {
						val[key] = scrubbed
					},
				})
				continue
			}
			if adapter.ScrubJSONValue(path, k) && isJSONScrubValue(child) {
				raw, err := json.Marshal(child)
				if err != nil {
					collectJSONScrubTasks(child, childPath, tasks, adapter)
					continue
				}
				key := k
				*tasks = append(*tasks, jsonScrubTask{
					text: string(raw),
					setText: func(scrubbed string) {
						var back any
						if json.Unmarshal([]byte(scrubbed), &back) == nil {
							val[key] = back
						}
					},
				})
				continue
			}
			collectJSONScrubTasks(child, childPath, tasks, adapter)
		}
	case []any:
		if adapter.ScrubArrayElement(path) {
			for i, child := range val {
				if s, ok := child.(string); ok {
					idx := i
					*tasks = append(*tasks, jsonScrubTask{
						text: s,
						setText: func(scrubbed string) {
							val[idx] = scrubbed
						},
					})
					continue
				}
				collectJSONScrubTasks(child, path, tasks, adapter)
			}
			return
		}
		for _, child := range val {
			collectJSONScrubTasks(child, path, tasks, adapter)
		}
	}
}

func isJSONScrubValue(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

func (r *Redactor) runJSONScrubTasks(
	ctx context.Context,
	tasks []jsonScrubTask,
	reg *Registry,
	forceRedactMarkers bool,
	acc *Result,
) error {
	if len(tasks) == 0 {
		return nil
	}
	limit := r.analyzeConcurrency()
	if len(tasks) == 1 || limit <= 1 {
		for i := range tasks {
			sub, err := r.scrub(ctx, tasks[i].text, reg, forceRedactMarkers)
			if err != nil {
				return err
			}
			tasks[i].setText(sub.Text)
			mergeScrubResult(acc, sub)
		}
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i := range tasks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				mu.Unlock()
				return
			}

			sub, err := r.scrub(ctx, tasks[i].text, reg, forceRedactMarkers)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mu.Unlock()
				return
			}

			mu.Lock()
			// setText writes into a shared parent container; sibling string
			// values in the same map would otherwise race (concurrent map
			// writes panic the process, unrecoverably).
			tasks[i].setText(sub.Text)
			mergeScrubResult(acc, sub)
			mu.Unlock()
		}(i)
	}

	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (r *Redactor) analyzeConcurrency() int {
	if r.cfg.AnalyzeConcurrency <= 0 {
		return defaultAnalyzeConcurrency
	}
	return r.cfg.AnalyzeConcurrency
}

func marshalJSONPreserveHTML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

func mergeScrubResult(dst *Result, src Result) {
	for k, v := range src.EntityCounts {
		dst.EntityCounts[k] += v
	}
	if len(src.DetectedEntities) > 0 {
		dst.DetectedEntities = append(dst.DetectedEntities, src.DetectedEntities...)
	}
	if len(src.AllowedEntities) > 0 {
		dst.AllowedEntities = append(dst.AllowedEntities, src.AllowedEntities...)
	}
}

func (r *Redactor) prefetchAnalyzeCache(ctx context.Context, tasks []jsonScrubTask) context.Context {
	if r.analyzeCache == nil || len(tasks) <= 1 {
		return ctx
	}
	adapter := AdapterForContext(ctx)
	texts := make([]string, len(tasks))
	for i := range tasks {
		texts[i] = prepareJSONForAnalysis(tasks[i].text, adapter)
	}
	return WithAnalyzeCachePrefetch(ctx, r.analyzeCache.GetMulti(ctx, texts))
}
