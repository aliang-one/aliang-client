package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/common/logger"
	clientcert "aliang.one/nursorgate/processor/cert/client"
	"aliang.one/nursorgate/processor/config"
	user "aliang.one/nursorgate/processor/auth"
)

type ChatHandler struct{}

const (
	chatRequestMaxBytes   int64 = 256 * 1024
	chatHistoryMaxEntries       = 20
)

func NewChatHandler() *ChatHandler {
	return &ChatHandler{}
}

type ChatRequest struct {
	Message string            `json:"message"`
	History []ChatHistoryItem `json:"history"`
}

type ChatHistoryItem struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatPayload struct {
	Model    string              `json:"model"`
	Messages []openAIMessageItem `json:"messages"`
}

type openAIMessageItem struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// mTLS HTTP/2 client for gateway forwarding (lazily initialized).
var (
	chatMTLSClient     *http.Client
	chatMTLSClientOnce sync.Once
	chatMTLSClientErr  error
)

func getOrCreateChatMTLSClient() (*http.Client, error) {
	chatMTLSClientOnce.Do(func() {
		gatewayURL := getGatewayChatURL()
		serverName := ""
		if gatewayURL != "" {
			if parsed, err := url.Parse(gatewayURL); err == nil {
				serverName = parsed.Hostname()
			}
		}
		if serverName == "" {
			chatMTLSClientErr = fmt.Errorf("cannot determine serverName from gateway URL")
			return
		}
		tlsConfig, err := clientcert.GetMTLSClientTLSConfig(true, serverName)
		if err != nil {
			chatMTLSClientErr = err
			return
		}
		transport := &http.Transport{
			TLSClientConfig: tlsConfig,
		}
		chatMTLSClient = &http.Client{
			Transport: transport,
			Timeout:   60 * time.Second,
		}
	})
	return chatMTLSClient, chatMTLSClientErr
}

// getGatewayChatURL builds the chat completions endpoint from core.api_server.
func getGatewayChatURL() string {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return ""
	}
	base := strings.TrimRight(cfg.APIBaseURL(), "/")
	if base == "" {
		return ""
	}
	return base + "/v1/chat/completions"
}

func (h *ChatHandler) HandleCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, chatRequestMaxBytes)

	var req ChatRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request format", nil)
		return
	}

	message := strings.TrimSpace(req.Message)
	if message == "" {
		common.ErrorBadRequest(w, "Message is required", nil)
		return
	}

	// Build message list from history.
	messages := make([]openAIMessageItem, 0, len(req.History)+1)
	for _, item := range req.History {
		role := strings.TrimSpace(strings.ToLower(item.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		messages = append(messages, openAIMessageItem{Role: role, Content: content})
	}
	if len(messages) > chatHistoryMaxEntries {
		messages = messages[len(messages)-chatHistoryMaxEntries:]
	}

	if len(messages) == 0 || messages[len(messages)-1].Role != "user" || strings.TrimSpace(messages[len(messages)-1].Content) != message {
		messages = append(messages, openAIMessageItem{Role: "user", Content: message})
	}

	payload := openAIChatPayload{
		Model:    "gpt-4o-mini",
		Messages: messages,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		logger.Error(fmt.Sprintf("[chat] marshal payload failed: %v", err))
		common.ErrorInternalServer(w, "Failed to build chat payload", nil)
		return
	}

	client, clientErr := getOrCreateChatMTLSClient()
	if clientErr != nil {
		common.ErrorInternalServer(w, "AI service unavailable", nil)
		return
	}

	gatewayURL := getGatewayChatURL()
	if gatewayURL == "" {
		common.ErrorInternalServer(w, "AI service not configured", nil)
		return
	}

	upstreamReq, err := http.NewRequest(http.MethodPost, gatewayURL, bytes.NewReader(bodyBytes))
	if err != nil {
		common.ErrorInternalServer(w, "Failed to build request", nil)
		return
	}
	upstreamReq.Header.Set("Content-Type", "application/json")

	// Inject Authorization-Inner.
	if authHeader := strings.TrimSpace(user.GetCurrentAuthorizationHeader()); authHeader != "" {
		upstreamReq.Header.Set("Authorization-Inner", authHeader)
	}

	resp, err := client.Do(upstreamReq)
	if err != nil {
		logger.Error(fmt.Sprintf("[chat] mTLS forward failed: %v", err))
		common.ErrorInternalServer(w, "AI service unavailable", nil)
		return
	}
	defer resp.Body.Close()

	h.writeAIResponse(w, resp)
}

// writeAIResponse reads the upstream response and writes the reply to the client.
func (h *ChatHandler) writeAIResponse(w http.ResponseWriter, upstreamResp *http.Response) {
	respBody, err := io.ReadAll(upstreamResp.Body)
	if err != nil {
		logger.Error(fmt.Sprintf("[chat] read AI response failed: %v", err))
		common.ErrorInternalServer(w, "Failed to read AI response", nil)
		return
	}

	if upstreamResp.StatusCode < 200 || upstreamResp.StatusCode >= 300 {
		logger.Error(fmt.Sprintf("[chat] AI service returned status=%d body=%s", upstreamResp.StatusCode, string(respBody)))
		common.ErrorInternalServer(w, "AI service returned error", nil)
		return
	}

	// Check if the response uses TLS and log the protocol.
	if upstreamResp.TLS != nil {
		logger.Debug(fmt.Sprintf("[chat] upstream TLS negotiated protocol: %v", upstreamResp.TLS.NegotiatedProtocol))
	}

	var parsed openAIResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		logger.Error(fmt.Sprintf("[chat] parse AI response failed: %v body=%s", err, string(respBody)))
		common.ErrorInternalServer(w, "Invalid AI response payload", nil)
		return
	}

	if len(parsed.Choices) == 0 {
		common.ErrorInternalServer(w, "AI response is empty", nil)
		return
	}

	reply := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if reply == "" {
		common.ErrorInternalServer(w, "AI response content is empty", nil)
		return
	}

	common.Success(w, map[string]interface{}{
		"reply": reply,
	})
}

// ResetChatMTLSClient resets the singleton mTLS client so it can be re-initialized.
// Useful for testing or when the config changes at runtime.
func ResetChatMTLSClient() {
	chatMTLSClientOnce = sync.Once{}
	chatMTLSClient = nil
	chatMTLSClientErr = nil
}
