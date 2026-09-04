package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

const mcpProtocolVersion = "2025-11-25"

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
	Permission  string         `json:"-"`
}

func (s *Server) mcpHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg, err := s.apiSettings(r)
		if err != nil || !cfg.Enabled || !cfg.MCPEnabled {
			writeMCP(w, http.StatusServiceUnavailable, mcpResponse{JSONRPC: "2.0", Error: &mcpError{Code: -32001, Message: "관리자가 MCP를 비활성화했습니다"}})
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Allow", http.MethodPost)
			writeMCP(w, http.StatusMethodNotAllowed, mcpResponse{JSONRPC: "2.0", Error: &mcpError{Code: -32600, Message: "이 배포는 POST JSON-RPC 전송을 사용합니다"}})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request mcpRequest
		if err := decoder.Decode(&request); err != nil {
			writeMCP(w, http.StatusBadRequest, mcpResponse{JSONRPC: "2.0", Error: &mcpError{Code: -32700, Message: "JSON-RPC 요청을 해석할 수 없습니다"}})
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || request.JSONRPC != "2.0" || request.Method == "" {
			writeMCP(w, http.StatusBadRequest, mcpResponse{JSONRPC: "2.0", ID: request.ID, Error: &mcpError{Code: -32600, Message: "올바르지 않은 JSON-RPC 요청입니다"}})
			return
		}
		if len(request.ID) == 0 && strings.HasPrefix(request.Method, "notifications/") {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		response := mcpResponse{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case "initialize":
			response.Result = map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]string{"name": "moina", "version": s.version},
				"instructions":    "MOINA의 Moin, Flow, Topic과 프로필을 현재 키 권한 범위 안에서 다룹니다.",
			}
		case "ping":
			response.Result = map[string]any{}
		case "tools/list":
			response.Result = map[string]any{"tools": visibleMCPTools(getPrincipal(r))}
		case "tools/call":
			result, callErr := s.callMCPTool(r, request.Params)
			if callErr != nil {
				response.Error = callErr
			} else {
				response.Result = result
			}
		default:
			response.Error = &mcpError{Code: -32601, Message: "지원하지 않는 MCP 메서드입니다"}
		}
		writeMCP(w, http.StatusOK, response)
	})
}

func writeMCP(w http.ResponseWriter, status int, response mcpResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func mcpTools() []mcpTool {
	object := func(properties map[string]any, required ...string) map[string]any {
		value := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			value["required"] = required
		}
		return value
	}
	stringProp := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return []mcpTool{
		{Name: "moina.flow.read", Title: "Flow 읽기", Description: "Following 또는 For Me Flow의 Moin을 읽습니다.", Permission: "posts:read", InputSchema: object(map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"for_me", "following"}}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}), Annotations: map[string]any{"readOnlyHint": true}},
		{Name: "moina.posts.get", Title: "Moin 조회", Description: "ID로 공개 가능한 Moin을 조회합니다.", Permission: "posts:read", InputSchema: object(map[string]any{"id": stringProp("Moin ID")}, "id"), Annotations: map[string]any{"readOnlyHint": true}},
		{Name: "moina.posts.search", Title: "Moin 검색", Description: "사람, Moin, Topic, Moim을 통합 검색합니다.", Permission: "posts:read", InputSchema: object(map[string]any{"query": stringProp("검색어"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "query"), Annotations: map[string]any{"readOnlyHint": true}},
		{Name: "moina.posts.create", Title: "Moin 작성", Description: "새 Moin을 작성합니다. 승인 정책이 켜져 있으면 승인 대기로 저장됩니다.", Permission: "posts:write", InputSchema: object(map[string]any{"content": stringProp("1~5000자 내용"), "visibility": map[string]any{"type": "string", "enum": []string{"public", "followers"}}}, "content"), Annotations: map[string]any{"destructiveHint": false}},
		{Name: "moina.echo.create", Title: "Echo 작성", Description: "기존 Moin에 Echo를 작성합니다.", Permission: "posts:write", InputSchema: object(map[string]any{"postId": stringProp("원문 Moin ID"), "content": stringProp("Echo 내용")}, "postId", "content"), Annotations: map[string]any{"destructiveHint": false}},
		{Name: "moina.profile.get", Title: "프로필 조회", Description: "사용자 이름으로 공개 프로필을 조회합니다.", Permission: "posts:read", InputSchema: object(map[string]any{"username": stringProp("사용자 이름")}, "username"), Annotations: map[string]any{"readOnlyHint": true}},
		{Name: "moina.topics.list", Title: "Topic 목록", Description: "인기 Topic 목록을 조회합니다.", Permission: "posts:read", InputSchema: object(map[string]any{"query": stringProp("선택 검색어"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}), Annotations: map[string]any{"readOnlyHint": true}},
		{Name: "moina.notifications.list", Title: "알림 목록", Description: "현재 사용자 알림을 조회합니다.", Permission: "posts:read", InputSchema: object(map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}), Annotations: map[string]any{"readOnlyHint": true}},
		{Name: "moina.ai.status", Title: "AI 상태", Description: "스트리밍 AI 공급자와 토큰 상한 상태를 조회합니다.", Permission: "ai:use", InputSchema: object(map[string]any{}), Annotations: map[string]any{"readOnlyHint": true}},
	}
}

func visibleMCPTools(p principal) []mcpTool {
	tools := make([]mcpTool, 0)
	for _, tool := range mcpTools() {
		if hasPermission(p.Permissions, tool.Permission) {
			tools = append(tools, tool)
		}
	}
	return tools
}

func (s *Server) callMCPTool(r *http.Request, raw json.RawMessage) (map[string]any, *mcpError) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(raw, &call) != nil || call.Name == "" {
		return nil, &mcpError{Code: -32602, Message: "도구 이름과 arguments가 필요합니다"}
	}
	var definition *mcpTool
	for _, candidate := range mcpTools() {
		if candidate.Name == call.Name {
			copy := candidate
			definition = &copy
			break
		}
	}
	if definition == nil {
		return nil, &mcpError{Code: -32602, Message: "존재하지 않는 MCP 도구입니다"}
	}
	if !hasPermission(getPrincipal(r).Permissions, definition.Permission) {
		return nil, &mcpError{Code: -32003, Message: "이 키에는 도구 실행 권한이 없습니다"}
	}
	arguments := map[string]any{}
	if len(call.Arguments) > 0 && json.Unmarshal(call.Arguments, &arguments) != nil {
		return nil, &mcpError{Code: -32602, Message: "도구 arguments가 올바르지 않습니다"}
	}
	if argumentErr := validateMCPArguments(definition.InputSchema, arguments); argumentErr != nil {
		return nil, argumentErr
	}
	var value any
	var err error
	switch call.Name {
	case "moina.flow.read":
		mode := stringArgument(arguments, "mode", "for_me")
		value, err = s.captureHandler(r, s.feed, url.Values{"mode": []string{mode}, "limit": []string{strconv.Itoa(intArgument(arguments, "limit", 30))}})
	case "moina.posts.get":
		id := stringArgument(arguments, "id", "")
		if id == "" {
			err = errors.New("id가 필요합니다")
		} else {
			value, err = s.loadMoin(r.Context(), id, getPrincipal(r).User.ID)
		}
	case "moina.posts.search":
		query := stringArgument(arguments, "query", "")
		value, err = s.captureHandler(r, s.search, url.Values{"q": []string{query}, "limit": []string{strconv.Itoa(intArgument(arguments, "limit", 30))}})
	case "moina.posts.create":
		value, err = s.createPostRecord(r, postInput{Content: stringArgument(arguments, "content", ""), Visibility: stringArgument(arguments, "visibility", "public")}, "")
	case "moina.echo.create":
		value, err = s.createPostRecord(r, postInput{Content: stringArgument(arguments, "content", ""), ReplyToID: stringArgument(arguments, "postId", ""), Visibility: "public"}, "echo")
	case "moina.profile.get":
		username := stringArgument(arguments, "username", "")
		if username == "" {
			err = errors.New("username이 필요합니다")
		} else {
			value, err = s.captureHandler(r, s.getProfile, nil, map[string]string{"username": username})
		}
	case "moina.topics.list":
		value, err = s.captureHandler(r, s.listTopics, url.Values{"q": []string{stringArgument(arguments, "query", "")}, "limit": []string{strconv.Itoa(intArgument(arguments, "limit", 30))}})
	case "moina.notifications.list":
		value, err = s.captureHandler(r, s.listNotifications, url.Values{"limit": []string{strconv.Itoa(intArgument(arguments, "limit", 30))}})
	case "moina.ai.status":
		value, err = s.captureHandler(r, s.aiStatus, nil)
	}
	if err != nil {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}, nil
	}
	encoded, _ := json.Marshal(value)
	s.audit(r, "mcp.tool.call", "mcp_tool", call.Name, true, nil)
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(encoded)}}, "structuredContent": value, "isError": false}, nil
}

func (s *Server) captureHandler(r *http.Request, handler http.HandlerFunc, query url.Values, params ...map[string]string) (any, error) {
	request := r.Clone(r.Context())
	request.Method = http.MethodGet
	request.Body = io.NopCloser(bytes.NewReader(nil))
	request.ContentLength = 0
	copyURL := *r.URL
	copyURL.RawQuery = query.Encode()
	request.URL = &copyURL
	if len(params) > 0 {
		for key, value := range params[0] {
			request = request.WithContext(contextWithURLParam(request.Context(), key, value))
		}
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	if recorder.Code < 200 || recorder.Code >= 300 {
		var apiErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &apiErr)
		if apiErr.Message == "" {
			apiErr.Message = "도구 실행에 실패했습니다"
		}
		return nil, errors.New(apiErr.Message)
	}
	var envelope struct {
		Data any `json:"data"`
	}
	if json.Unmarshal(recorder.Body.Bytes(), &envelope) != nil {
		return nil, errors.New("도구 응답을 해석할 수 없습니다")
	}
	return envelope.Data, nil
}

// An MCP client is an agent, and an agent cannot see that one of its arguments
// was dropped. A limit of 500, a limit sent as the string "50" and a limit
// misspelled as "count" all used to fall back to 30, so the tool answered a
// different question than the one asked and the agent read the short list as
// the whole account. Every tool already publishes an inputSchema in
// tools/list; that schema is the contract, so it is what a call is checked
// against - the same strictness the REST collections gained with
// invalid_pagination. Invalid arguments are a JSON-RPC error rather than a
// tool result, which is how the MCP specification separates them from a tool
// that ran and failed.
func validateMCPArguments(schema, arguments map[string]any) *mcpError {
	properties, _ := schema["properties"].(map[string]any)
	names := make([]string, 0, len(arguments))
	for name := range arguments {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		definition, ok := properties[name].(map[string]any)
		if !ok {
			return &mcpError{Code: -32602, Message: fmt.Sprintf("%s는 이 도구가 받지 않는 인자입니다", name)}
		}
		if message := mcpArgumentError(name, definition, arguments[name]); message != "" {
			return &mcpError{Code: -32602, Message: message}
		}
	}
	required, _ := schema["required"].([]string)
	for _, name := range required {
		if _, ok := arguments[name]; !ok {
			return &mcpError{Code: -32602, Message: fmt.Sprintf("%s는 필수 인자입니다", name)}
		}
	}
	return nil
}

func mcpArgumentError(name string, definition map[string]any, value any) string {
	switch definition["type"] {
	case "integer":
		number, ok := value.(float64)
		if !ok || number != math.Trunc(number) {
			return fmt.Sprintf("%s는 정수여야 합니다", name)
		}
		if minimum, ok := definition["minimum"].(int); ok && number < float64(minimum) {
			return fmt.Sprintf("%s는 %d 이상이어야 합니다", name, minimum)
		}
		if maximum, ok := definition["maximum"].(int); ok && number > float64(maximum) {
			return fmt.Sprintf("%s는 %d 이하여야 합니다", name, maximum)
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Sprintf("%s는 문자열이어야 합니다", name)
		}
		// stringArgument trims before use, so the enum is checked against the
		// same value the tool will actually run with.
		if allowed, ok := definition["enum"].([]string); ok && !slicesContains(allowed, strings.TrimSpace(text)) {
			return fmt.Sprintf("%s는 %s 중 하나여야 합니다", name, strings.Join(allowed, ", "))
		}
	}
	return ""
}

func stringArgument(values map[string]any, key, fallback string) string {
	if value, ok := values[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

// The schema check already rejected a non-integer or out of range value, so
// this only has to supply the default for an argument that was left out.
func intArgument(values map[string]any, key string, fallback int) int {
	if value, ok := values[key].(float64); ok {
		return int(value)
	}
	return fallback
}

func contextWithURLParam(ctx context.Context, key, value string) context.Context {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add(key, value)
	return context.WithValue(ctx, chi.RouteCtxKey, routeContext)
}
