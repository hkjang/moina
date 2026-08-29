package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
)

const absoluteMaxAITokens = 262144

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatInput struct {
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"maxTokens"`
}

func validateAI(cfg *model.AIConfig) error {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.APIStyle = strings.ToLower(strings.TrimSpace(cfg.APIStyle))
	if cfg.APIStyle == "" {
		cfg.APIStyle = "responses"
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = absoluteMaxAITokens
	}
	if cfg.DefaultMaxTokens == 0 {
		cfg.DefaultMaxTokens = 4096
	}
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = 300
	}
	if !cfg.Enabled {
		return nil
	}
	if cfg.BaseURL == "" || cfg.Model == "" || !slicesContains([]string{"responses", "chat_completions"}, cfg.APIStyle) {
		return errors.New("활성화하려면 API 주소, 모델, API 방식을 입력해야 합니다")
	}
	if err := validateServiceURL(cfg.BaseURL, cfg.AllowInsecureHTTP); err != nil {
		return fmt.Errorf("baseUrl: %w", err)
	}
	if cfg.DefaultMaxTokens < 1 || cfg.MaxTokens < cfg.DefaultMaxTokens || cfg.MaxTokens > absoluteMaxAITokens {
		return errors.New("토큰은 1 <= 기본값 <= 서비스 상한 <= 262144여야 합니다")
	}
	if cfg.TimeoutSeconds < 10 || cfg.TimeoutSeconds > 3600 {
		return errors.New("요청 제한 시간은 10~3600초여야 합니다")
	}
	return nil
}

func (s *Server) aiStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.aiConfig(r)
	if err != nil || validateAI(&cfg) != nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "AI 설정을 불러올 수 없습니다")
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"enabled": cfg.Enabled, "model": cfg.Model, "apiStyle": cfg.APIStyle,
		"defaultMaxTokens": cfg.DefaultMaxTokens, "maxTokens": cfg.MaxTokens, "streaming": true,
	})
}

func (s *Server) aiChat(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	cfg, err := s.aiConfig(r)
	if err != nil || validateAI(&cfg) != nil || !cfg.Enabled {
		writeError(w, http.StatusServiceUnavailable, "ai_disabled", "AI 기능이 설정되지 않았거나 비활성화되었습니다")
		return
	}
	var input chatInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.MaxTokens == 0 {
		input.MaxTokens = cfg.DefaultMaxTokens
	}
	if input.MaxTokens < 1 || input.MaxTokens > cfg.MaxTokens || input.MaxTokens > absoluteMaxAITokens || len(input.Messages) < 1 || len(input.Messages) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_ai_request", "메시지 또는 최대 토큰 설정이 올바르지 않습니다")
		return
	}
	totalRunes := 0
	for _, message := range input.Messages {
		if !slicesContains([]string{"system", "user", "assistant"}, message.Role) || !utf8.ValidString(message.Content) || strings.ContainsRune(message.Content, '\x00') {
			writeError(w, http.StatusBadRequest, "invalid_messages", "AI 메시지 형식이 올바르지 않습니다")
			return
		}
		totalRunes += utf8.RuneCountInString(message.Content)
	}
	if totalRunes > 1_000_000 {
		writeError(w, http.StatusRequestEntityTooLarge, "messages_too_large", "AI 대화 내용이 너무 큽니다")
		return
	}

	endpoint := cfg.BaseURL + "/responses"
	payload := map[string]any{"model": cfg.Model, "input": input.Messages, "max_output_tokens": input.MaxTokens, "stream": true}
	if cfg.APIStyle == "chat_completions" {
		endpoint = cfg.BaseURL + "/chat/completions"
		payload = map[string]any{"model": cfg.Model, "messages": input.Messages, "max_tokens": input.MaxTokens, "stream": true}
	}
	body, _ := json.Marshal(payload)
	ctx, cancel := contextWithTimeout(r, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadGateway, "ai_request_failed", "AI 요청을 만들 수 없습니다")
		return
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	response, err := s.client.Do(request)
	if err != nil {
		s.recordAIUsage(r, cfg, input.MaxTokens, false, time.Since(started))
		writeError(w, http.StatusBadGateway, "ai_unavailable", "AI 공급자에 연결할 수 없습니다")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
		s.recordAIUsage(r, cfg, input.MaxTokens, false, time.Since(started))
		writeError(w, http.StatusBadGateway, "ai_upstream_error", "AI 공급자가 요청을 거부했습니다")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "이 서버는 스트리밍을 지원하지 않습니다")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	buffer := make([]byte, 32<<10)
	success := true
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if _, writeErr := w.Write(buffer[:count]); writeErr != nil {
				success = false
				break
			}
			flusher.Flush()
		}
		if readErr != nil {
			if readErr != io.EOF {
				success = false
			}
			break
		}
	}
	s.recordAIUsage(r, cfg, input.MaxTokens, success, time.Since(started))
}

func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}

func (s *Server) recordAIUsage(r *http.Request, cfg model.AIConfig, maxTokens int, success bool, elapsed time.Duration) {
	_, _ = s.repo.Pool().Exec(r.Context(), `INSERT INTO ai_usage_events(id,user_id,model,api_style,max_tokens,success,latency_ms) VALUES($1,$2,$3,$4,$5,$6,$7)`, secure.NewID("aiu"), getPrincipal(r).User.ID, cfg.Model, cfg.APIStyle, maxTokens, success, elapsed.Milliseconds())
}

func (s *Server) adminTestAI(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.aiConfig(r)
	if err != nil || validateAI(&cfg) != nil || !cfg.Enabled {
		writeError(w, http.StatusBadRequest, "ai_disabled", "저장된 AI 설정을 먼저 활성화해 주세요")
		return
	}
	parsed, _ := url.Parse(cfg.BaseURL + "/models")
	ctx, cancel := contextWithTimeout(r, min(time.Duration(cfg.TimeoutSeconds)*time.Second, 30*time.Second))
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	response, err := s.client.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ai_unavailable", "AI 공급자에 연결할 수 없습니다")
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, "ai_test_failed", "AI 공급자의 모델 API가 정상 응답하지 않았습니다")
		return
	}
	writeData(w, http.StatusOK, map[string]any{"connected": true, "model": cfg.Model, "streaming": true})
}
