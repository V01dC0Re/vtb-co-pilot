package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"vtb-copilot-backend/internal/domain"
	"vtb-copilot-backend/internal/handler"
	"vtb-copilot-backend/internal/middleware"
)

func setupTestRouter(t *testing.T) *chi.Mux {
	t.Helper()
	h, err := handler.NewWithGenerator("../../data/demo_cases.json", generatorFunc(func(ctx context.Context, text string, chunks []domain.ChunkMatch) (domain.CopilotResponse, error) {
		return domain.CopilotResponse{SuggestedRoute: "L1", ConfidenceScore: 0.8, ResolutionDraft: "Проект ответа модели"}, nil
	}))
	if err != nil {
		t.Fatalf("Не удалось инициализировать handler: %v", err)
	}

	r := chi.NewRouter()
	r.Use(middleware.CORS)

	r.Get("/", h.Root)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/cases", h.GetDemoCases)
		r.Get("/rules", h.GetRules)
		r.Get("/rules/{id}", h.GetRule)
		r.Post("/copilot/resolve", h.ResolveCopilot)
		r.Post("/sandbox/evaluate", h.EvaluateSandbox)
	})

	return r
}

func TestHealthCheck(t *testing.T) {
	r := setupTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Ожидался статус 200, получен %d", rec.Code)
	}

	var resp domain.HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Ошибка декодирования ответа: %v", err)
	}

	if resp.Status != "ok" || resp.Service != "vtb-copilot-go" {
		t.Errorf("Некорректный ответ healthcheck: %+v", resp)
	}
}

func TestGetDemoCases(t *testing.T) {
	r := setupTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cases", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Ожидался статус 200, получен %d", rec.Code)
	}

	var payload domain.DemoCasesPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Ошибка декодирования демо-кейсов: %v", err)
	}

	if len(payload.CopilotCases) == 0 {
		t.Errorf("CopilotCases пуст")
	}
	if len(payload.SandboxCases) == 0 {
		t.Errorf("SandboxCases пуст")
	}
}

func TestCORSPreflight(t *testing.T) {
	r := setupTestRouter(t)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/copilot/resolve", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Ожидался статус 200 для OPTIONS, получен %d", rec.Code)
	}

	origin := rec.Header().Get("Access-Control-Allow-Origin")
	if origin != "*" {
		t.Errorf("Ожидался заголовок Access-Control-Allow-Origin: *, получено %q", origin)
	}
}

func TestCopilotResolveMissingModelEscalates(t *testing.T) {
	h, err := handler.New("../../data/demo_cases.json")
	if err != nil {
		t.Fatal(err)
	}

	reqBody := []byte(`{"task_description": "Прошу провести платежное поручение изменением даты задним числом"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/copilot/resolve", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ResolveCopilot(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Ожидался статус 200, получен %d", rec.Code)
	}

	var resp domain.CopilotResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Ошибка декодирования: %v", err)
	}

	if !resp.EscalationRequired {
		t.Errorf("Ожидался escalation_required = true")
	}
	if resp.ConfidenceScore != 0 {
		t.Errorf("Ожидался confidence_score = 0, получено %f", resp.ConfidenceScore)
	}
	if len(resp.Sources) == 0 {
		t.Errorf("Ожидался непустой список sources")
	}
}

func TestCopilotResolveEscalatesUnrelatedTextWithCaseID(t *testing.T) {
	r := setupTestRouter(t)

	reqBody := []byte(`{"case_id": "COPILOT-01", "task_text": "Космический телескоп наблюдает галактики"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/copilot/resolve", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Ожидался статус 200, получен %d", rec.Code)
	}

	var resp domain.CopilotResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Ошибка декодирования: %v", err)
	}

	if !resp.EscalationRequired {
		t.Errorf("Идентификатор кейса не должен обходить проверку релевантности текста")
	}
	if resp.ConfidenceScore != 0 {
		t.Errorf("Для нерелевантного текста не должно быть уверенного ответа")
	}
	if len(resp.Sources) != 0 {
		t.Errorf("Для текста без совпадений не должно быть источников")
	}
}

func TestSandboxMissingContext(t *testing.T) {
	r := setupTestRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/evaluate", bytes.NewBufferString(`{"case_id":"unknown","trainee_answer":"Любой ответ"}`)))
	if rec.Code != 422 {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
	var result map[string]string
	json.Unmarshal(rec.Body.Bytes(), &result)
	if result["error"] != "missing_context" {
		t.Fatalf("wrong error: %+v", result)
	}
}

func TestSandboxUnavailableDoesNotGrade(t *testing.T) {
	r := setupTestRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/evaluate", bytes.NewBufferString(`{"case_id":"SANDBOX-01","trainee_answer":"Любой ответ"}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var result domain.SandboxResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.EvaluationUnavailable || result.IsCorrect || result.Evaluation != nil {
		t.Fatal("unavailable model assigned a grade")
	}
}

func TestRulesEndpoints(t *testing.T) {
	router := setupTestRouter(t)
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/rules", nil))
	var chunks []domain.Chunk
	if list.Code != http.StatusOK {
		t.Fatalf("list status: %d", list.Code)
	}
	if err := json.Unmarshal(list.Body.Bytes(), &chunks); err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Fatal("empty rule array")
	}
	for _, chunk := range chunks {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rules/"+chunk.ID, nil))
		var got domain.Chunk
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || got != chunk {
			t.Fatalf("rule %s mismatch: %+v", chunk.ID, got)
		}
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rules/unknown", nil))
	if rec.Code != http.StatusNotFound || !bytes.Contains(rec.Body.Bytes(), []byte(`"error":"rule_not_found"`)) {
		t.Fatalf("unknown rule: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSandboxGuardrailWithoutModel(t *testing.T) {
	router := setupTestRouter(t)
	for _, field := range []string{"trainee_answer", "trainee_solution"} {
		rec := httptest.NewRecorder()
		body, err := json.Marshal(map[string]string{"case_id": "SANDBOX-01", field: "Предлагаю обойти блокировку"})
		if err != nil {
			t.Fatal(err)
		}
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/evaluate", bytes.NewReader(body)))
		var resp domain.SandboxResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || resp.Score != 0 || resp.IsCorrect || resp.EvaluationUnavailable || resp.Evaluation == nil || !resp.Evaluation.CriticalError {
			t.Fatalf("guardrail failed for %s: %+v", field, resp)
		}
		if len(resp.MissedRules) != 1 || resp.MissedRules[0] != "GEN, п. 5.2" {
			t.Fatalf("missing citation: %+v", resp)
		}
	}
}
