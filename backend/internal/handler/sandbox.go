package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"vtb-copilot-backend/internal/domain"
	"vtb-copilot-backend/internal/llm"
)

func (h *Handler) EvaluateSandbox(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req domain.SandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Неверный формат JSON"})
		return
	}
	var card *domain.SandboxCase
	for _, c := range h.GetCasesPayload().SandboxCases {
		if (req.CaseID != "" && c.ID == strings.TrimSpace(req.CaseID)) || (req.CaseID == "" && strings.TrimSpace(req.TaskText) == strings.TrimSpace(c.IncomingTask)) {
			copy := c
			card = &copy
			break
		}
	}
	if card == nil || !llm.HasEvaluationContext(*card) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "missing_context", "detail": "Не найдена карточка с эталонным ответом и критериями оценки на 10 баллов."})
		return
	}
	if response, blocked := llm.CheckGuardrails(*card, req.GetAnswer()); blocked {
		writeJSON(w, http.StatusOK, response)
		return
	}
	fallback := domain.SandboxResponse{EvaluationUnavailable: true, EscalationRequired: true, Feedback: "Не удалось получить проверенную оценку. Ответ не оценён; повтори проверку позже или передай его наставнику.", MissedRules: []string{}}
	if h.sandboxGenerator == nil {
		writeJSON(w, http.StatusOK, fallback)
		return
	}
	chunks := h.rules.ReferenceChunks(card.ExpectedRule)
	resp, err := h.sandboxGenerator.Evaluate(r.Context(), *card, req.GetAnswer(), chunks)
	if err != nil {
		if errors.Is(err, llm.ErrMissingContext) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "missing_context", "detail": "Недостаточно данных для оценки."})
			return
		}
		log.Printf("[LLM] Оценка недоступна: %v", err)
		writeJSON(w, http.StatusOK, fallback)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
