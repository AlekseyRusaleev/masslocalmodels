package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/private-compute/network/internal/store"
)

// maxMessagesBodyBytes — предел тела для сохранения хода диалога.
//
// Ход — это вопрос и ответ модели; ответ с размышлениями легко занимает сотни
// килобайт. Ограничение остаётся: без него один запрос мог бы занять память.
const maxMessagesBodyBytes = 4 << 20

// maxConversationTitle — длина заголовка диалога.
const maxConversationTitle = 4000

// registerConversationRoutes подключает историю диалогов.
//
// Диалог принадлежит аккаунту: и список, и чтение, и удаление фильтруются по
// владельцу, поэтому чужой идентификатор отвечает так же, как несуществующий.
func (cfg Config) registerConversationRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/conversations", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		conversations, err := cfg.Store.ListConversations(r.Context(), p.UserID, 50)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot list conversations")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": conversations})
	}))

	mux.Handle("POST /v1/conversations", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		var body struct {
			Title      string `json:"title"`
			Model      string `json:"model"`
			TitleNonce string `json:"title_nonce"`
			TitleKeyID string `json:"title_key_id"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		conversation, err := cfg.Store.CreateConversation(r.Context(), p.UserID,
			truncate(strings.TrimSpace(body.Title), maxConversationTitle), strings.TrimSpace(body.Model),
			strings.TrimSpace(body.TitleNonce), strings.TrimSpace(body.TitleKeyID))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot create conversation")
			return
		}
		writeJSON(w, http.StatusCreated, conversation)
	}))

	mux.Handle("GET /v1/conversations/{id}", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		id := r.PathValue("id")
		conversation, err := cfg.Store.ConversationByID(r.Context(), p.UserID, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "conversation not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot load conversation")
			return
		}
		messages, err := cfg.Store.MessagesByConversation(r.Context(), p.UserID, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "cannot load messages")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"conversation": conversation,
			"messages":     messages,
		})
	}))

	mux.Handle("PATCH /v1/conversations/{id}", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		id := r.PathValue("id")
		var body struct {
			Title      string `json:"title"`
			TitleNonce string `json:"title_nonce"`
			TitleKeyID string `json:"title_key_id"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		title := truncate(strings.TrimSpace(body.Title), maxConversationTitle)
		if title == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "title is required")
			return
		}
		if err := cfg.Store.RenameConversation(r.Context(), p.UserID, id, title,
			strings.TrimSpace(body.TitleNonce), strings.TrimSpace(body.TitleKeyID)); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "conversation not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot rename conversation")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "title": title})
	}))

	mux.Handle("DELETE /v1/conversations/{id}", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		id := r.PathValue("id")
		if err := cfg.Store.DeleteConversation(r.Context(), p.UserID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "conversation not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot delete conversation")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	}))

	mux.Handle("POST /v1/conversations/{id}/messages", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		id := r.PathValue("id")
		var body struct {
			Messages []store.MessageInput `json:"messages"`
		}
		if err := decodeJSONLimit(w, r, &body, maxMessagesBodyBytes); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if len(body.Messages) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "messages are required")
			return
		}
		for _, message := range body.Messages {
			switch message.Role {
			case "user", "assistant", "system":
			default:
				writeError(w, http.StatusBadRequest, "invalid_request", "role must be user, assistant or system")
				return
			}
			sealed := strings.TrimSpace(message.Ciphertext) != "" &&
				strings.TrimSpace(message.Nonce) != "" &&
				strings.TrimSpace(message.KeyID) != ""
			if !sealed {
				writeError(w, http.StatusBadRequest, "plaintext_rejected",
					"message text must be ciphertext: the key stays on the participant's computer")
				return
			}
			if strings.TrimSpace(message.Content) != "" || strings.TrimSpace(message.Reasoning) != "" {
				writeError(w, http.StatusBadRequest, "plaintext_rejected",
					"content and reasoning must be empty when ciphertext is stored")
				return
			}
		}
		saved, err := cfg.Store.AppendMessages(r.Context(), p.UserID, id, body.Messages)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "conversation not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot save messages")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"object": "list", "data": saved})
	}))

	mux.Handle("POST /v1/conversations/{id}/messages/{messageID}/seal", cfg.requireAuth("chat:write", func(w http.ResponseWriter, r *http.Request, p principal) {
		var body struct {
			KeyID      string `json:"key_id"`
			Nonce      string `json:"nonce"`
			Ciphertext string `json:"ciphertext"`
		}
		if err := decodeJSONLimit(w, r, &body, maxMessagesBodyBytes); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if strings.TrimSpace(body.KeyID) == "" || strings.TrimSpace(body.Nonce) == "" || strings.TrimSpace(body.Ciphertext) == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "key_id, nonce and ciphertext are required")
			return
		}
		err := cfg.Store.SealMessage(r.Context(), p.UserID, r.PathValue("id"), r.PathValue("messageID"),
			strings.TrimSpace(body.KeyID), strings.TrimSpace(body.Nonce), body.Ciphertext)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "message not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal", "cannot seal message")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sealed": true})
	}))
}
