package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Pedro-Revez-Silva/subtitler/internal/subtitle"
)

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func New(apiKey, baseURL string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 20 * time.Minute},
	}
}

func (c *Client) TranscribeSRT(ctx context.Context, audioPath, model, prompt, language string) (string, error) {
	if model != "whisper-1" {
		return "", fmt.Errorf("model %q is configured, but timestamped SRT output currently requires whisper-1", model)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", model); err != nil {
		return "", err
	}
	if err := writer.WriteField("response_format", "srt"); err != nil {
		return "", err
	}
	if prompt != "" {
		if err := writer.WriteField("prompt", prompt); err != nil {
			return "", err
		}
	}
	if language != "" && language != "auto" {
		if err := writer.WriteField("language", language); err != nil {
			return "", err
		}
	}

	file, err := os.Open(audioPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	part, err := writer.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("OpenAI transcription failed: %s: %s", resp.Status, string(respBody))
	}
	return string(respBody), nil
}

func (c *Client) TranslateSRT(ctx context.Context, srt, model, targetLanguage, contextHint string) (string, error) {
	prompt := fmt.Sprintf(`Translate this SRT subtitle file to %s.

Rules:
- Preserve cue numbers exactly.
- Preserve timestamps exactly.
- Preserve blank lines between cues.
- Translate only subtitle text.
- Do not wrap the answer in Markdown.
- Keep names, fictional terms, and invented expressions consistent.

Context:
%s

SRT:
%s`, targetLanguage, contextHint, srt)

	payload := map[string]any{
		"model": model,
		"input": prompt,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("OpenAI translation failed: %s: %s", resp.Status, string(respBody))
	}

	var parsed responsesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", err
	}
	if strings.TrimSpace(parsed.OutputText) != "" {
		return parsed.OutputText, nil
	}
	for _, output := range parsed.Output {
		for _, content := range output.Content {
			if content.Text != "" {
				return content.Text, nil
			}
		}
	}
	return "", fmt.Errorf("OpenAI translation response did not include output text")
}

func (c *Client) TranslateCues(ctx context.Context, cues []subtitle.Cue, model, targetLanguage, contextHint string) ([]subtitle.Cue, error) {
	out := make([]subtitle.Cue, len(cues))
	copy(out, cues)

	translations, err := c.translateCueRange(ctx, cues, model, targetLanguage, contextHint)
	if err != nil {
		return nil, err
	}
	for idx := range out {
		text, ok := translations[out[idx].Index]
		if !ok {
			return nil, fmt.Errorf("translation response missing cue %d", out[idx].Index)
		}
		out[idx].Text = text
	}
	return out, nil
}

func (c *Client) translateCueRange(ctx context.Context, cues []subtitle.Cue, model, targetLanguage, contextHint string) (map[int]string, error) {
	const batchSize = 20
	translations := make(map[int]string, len(cues))
	for start := 0; start < len(cues); start += batchSize {
		end := start + batchSize
		if end > len(cues) {
			end = len(cues)
		}
		batchTranslations, err := c.translateCueBatchResilient(ctx, cues[start:end], model, targetLanguage, contextHint)
		if err != nil {
			return nil, err
		}
		for id, text := range batchTranslations {
			translations[id] = text
		}
	}
	return translations, nil
}

func (c *Client) translateCueBatchResilient(ctx context.Context, cues []subtitle.Cue, model, targetLanguage, contextHint string) (map[int]string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		translations, err := c.translateCueBatch(ctx, cues, model, targetLanguage, contextHint)
		if err == nil {
			return translations, nil
		}
		lastErr = err
	}

	if len(cues) <= 1 {
		return nil, lastErr
	}

	mid := len(cues) / 2
	left, err := c.translateCueBatchResilient(ctx, cues[:mid], model, targetLanguage, contextHint)
	if err != nil {
		return nil, err
	}
	right, err := c.translateCueBatchResilient(ctx, cues[mid:], model, targetLanguage, contextHint)
	if err != nil {
		return nil, err
	}
	for id, text := range right {
		left[id] = text
	}
	return left, nil
}

func (c *Client) translateCueBatch(ctx context.Context, cues []subtitle.Cue, model, targetLanguage, contextHint string) (map[int]string, error) {
	items := make([]cueTranslationItem, 0, len(cues))
	for _, cue := range cues {
		items = append(items, cueTranslationItem{ID: cue.Index, Text: cue.Text})
	}
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}

	prompt := fmt.Sprintf(`Translate each subtitle cue text to %s.

Rules:
- Return only valid JSON.
- Return exactly this shape: {"translations":[{"id":1,"text":"translated text"}]}.
- Include exactly one translation for every input id.
- Do not add, remove, merge, split, or reorder cues.
- Do not include timestamps.
- Translate only the text field.
- Keep subtitle text concise and natural.
- Preserve character names and invented terms consistently.

Context:
%s

Input JSON:
%s`, targetLanguage, contextHint, string(itemsJSON))

	payload := map[string]any{
		"model": model,
		"input": prompt,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OpenAI cue translation failed: %s: %s", resp.Status, string(respBody))
	}

	text, err := responseText(respBody)
	if err != nil {
		return nil, err
	}
	var parsed cueTranslationResponse
	if err := json.Unmarshal([]byte(cleanJSON(text)), &parsed); err != nil {
		return nil, fmt.Errorf("parse cue translation JSON: %w", err)
	}
	if len(parsed.Translations) != len(cues) {
		return nil, fmt.Errorf("translation returned %d cues, expected %d", len(parsed.Translations), len(cues))
	}

	translations := make(map[int]string, len(parsed.Translations))
	for _, item := range parsed.Translations {
		translations[item.ID] = strings.TrimSpace(item.Text)
	}
	return translations, nil
}

type responsesResponse struct {
	OutputText string `json:"output_text"`
	Output     []struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

type cueTranslationItem struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type cueTranslationResponse struct {
	Translations []cueTranslationItem `json:"translations"`
}

func responseText(data []byte) (string, error) {
	var parsed responsesResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if strings.TrimSpace(parsed.OutputText) != "" {
		return parsed.OutputText, nil
	}
	for _, output := range parsed.Output {
		for _, content := range output.Content {
			if content.Text != "" {
				return content.Text, nil
			}
		}
	}
	return "", fmt.Errorf("OpenAI response did not include output text")
}

func cleanJSON(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "```json")
	value = strings.TrimPrefix(value, "```")
	value = strings.TrimSuffix(value, "```")
	return strings.TrimSpace(value)
}
