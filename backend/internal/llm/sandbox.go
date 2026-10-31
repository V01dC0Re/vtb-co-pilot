package llm

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"vtb-copilot-backend/internal/domain"
)

var criticalMarkerPattern = regexp.MustCompile(`(?i)(?:систем[а-яё]*\s+антифрод[а-яё]*|подозрительн[а-яё]*\s+получател[а-яё]*|порог[а-яё]*\s+сумм[а-яё]*|обойти\s+блокировк[а-яё]*)`)

// CheckGuardrails applies a conservative marker policy to the trainee answer only.
// This is also used by the HTTP handler when no model is configured.
func CheckGuardrails(card domain.SandboxCase, answer string) (domain.SandboxResponse, bool) {
	if !criticalMarkerPattern.MatchString(answer) {
		return domain.SandboxResponse{}, false
	}
	citation := "GEN, п. 5.2"
	feedback := "Обнаружен критический маркер раскрытия внутренних признаков проверок или обхода блокировки. Не раскрывай клиенту критерии антифрода и комплаенса (GEN, п. 5.2)."
	evaluation := domain.SandboxEvaluation{
		CaseID: &card.ID, Score: 0, MaxScore: 10, Passed: false, CriticalError: true,
		CriteriaResults: make([]domain.CriterionResult, 0, len(card.EvaluationCriteria)),
		MissedPoints:    make([]domain.MissedPoint, 0, len(card.EvaluationCriteria)),
		ErrorsFound:     []domain.EvaluationError{{Description: feedback, Critical: true, Citation: &citation}},
		Feedback:        feedback, MainLesson: "Не раскрывай внутренние критерии проверок и не предлагай обход блокировки.",
		Flags: []string{"guardrail_critical_marker"},
	}
	for _, criterion := range card.EvaluationCriteria {
		evaluation.CriteriaResults = append(evaluation.CriteriaResults, domain.CriterionResult{
			Criterion: criterion.Criterion, MaxPoints: criterion.Points, AwardedPoints: 0,
			Verdict: "не выполнено", Comment: "Оценка обнулена из-за критического маркера безопасности.",
		})
		evaluation.MissedPoints = append(evaluation.MissedPoints, domain.MissedPoint{
			Point: "Соблюдай запрет на раскрытие внутренних критериев проверок.", Criterion: criterion.Criterion, Citation: &citation,
		})
	}
	return domain.SandboxResponse{
		Evaluation: &evaluation, Score: 0, IsCorrect: false, Feedback: feedback, MissedRules: []string{citation},
	}, true
}

func HasEvaluationContext(card domain.SandboxCase) bool {
	if strings.TrimSpace(card.ReferenceAnswer) == "" || len(card.EvaluationCriteria) == 0 {
		return false
	}
	sum := 0
	seen := map[string]bool{}
	for _, criterion := range card.EvaluationCriteria {
		if strings.TrimSpace(criterion.Criterion) == "" || criterion.Points < 1 || criterion.Points > 10 || seen[criterion.Criterion] {
			return false
		}
		sum += criterion.Points
		seen[criterion.Criterion] = true
	}
	return sum == 10
}

func (c *Client) Evaluate(ctx context.Context, card domain.SandboxCase, answer string, chunks []domain.ChunkMatch) (domain.SandboxResponse, error) {
	if !HasEvaluationContext(card) {
		return domain.SandboxResponse{}, ErrMissingContext
	}
	if response, blocked := CheckGuardrails(card, answer); blocked {
		return response, nil
	}
	criteria := make([]map[string]any, 0, len(card.EvaluationCriteria))
	for _, criterion := range card.EvaluationCriteria {
		criteria = append(criteria, map[string]any{"criterion": criterion.Criterion, "max_points": criterion.Points})
	}
	contextJSON, _ := json.Marshal(map[string]any{"id": card.ID, "reference_answer": card.ReferenceAnswer, "expected_rule": card.ExpectedRule, "typical_mistakes": card.TypicalMistakes, "evaluation_criteria": criteria, "rules": chunks})
	task := card.IncomingTask
	if card.TraineeTask != "" {
		task += "\nЗадание стажёру: " + card.TraineeTask
	}
	data, err := c.call(ctx, renderPrompt(sandboxPrompt, string(contextJSON), task, answer), sandboxSchema(card, chunks))
	if err != nil {
		return domain.SandboxResponse{}, err
	}
	return parseEvaluation(data, card, chunks)
}

func sandboxSchema(card domain.SandboxCase, chunks []domain.ChunkMatch) map[string]any {
	names := make([]string, 0, len(card.EvaluationCriteria))
	for _, c := range card.EvaluationCriteria {
		names = append(names, c.Criterion)
	}
	citations := make([]string, 0, len(chunks))
	for _, c := range chunks {
		citations = append(citations, c.Citation)
	}
	citationSchema := map[string]any{"type": "null"}
	if len(citations) > 0 {
		citationSchema = nullable(enumeration(citations...))
	}
	return obj(map[string]any{
		"case_id": enumeration(card.ID), "score": integer(0, 10), "max_score": integer(10, 10), "passed": boolean(), "critical_error": boolean(),
		"criteria_results": array(obj(map[string]any{"criterion": enumeration(names...), "max_points": integer(1, 10), "awarded_points": integer(0, 10), "verdict": enumeration("выполнено", "частично", "не выполнено"), "comment": str()})),
		"missed_points":    array(obj(map[string]any{"point": str(), "criterion": enumeration(names...), "citation": citationSchema})),
		"errors_found":     array(obj(map[string]any{"description": str(), "critical": boolean(), "citation": citationSchema})),
		"feedback":         str(), "main_lesson": str(), "flags": array(str()),
	})
}

func parseEvaluation(data []byte, card domain.SandboxCase, chunks []domain.ChunkMatch) (domain.SandboxResponse, error) {
	var evaluation domain.SandboxEvaluation
	if err := decodeStrict(data, sandboxSchema(card, chunks), &evaluation); err != nil {
		return domain.SandboxResponse{}, err
	}
	invalid := func() (domain.SandboxResponse, error) { return domain.SandboxResponse{}, ErrInvalidResponse }
	if evaluation.CaseID == nil || *evaluation.CaseID != card.ID || len(evaluation.CriteriaResults) != len(card.EvaluationCriteria) {
		return invalid()
	}
	sum := 0
	missed := map[string]bool{}
	covered := map[string]bool{}
	for i, criterion := range evaluation.CriteriaResults {
		expected := card.EvaluationCriteria[i]
		if criterion.Criterion != expected.Criterion || criterion.MaxPoints != expected.Points || criterion.AwardedPoints > expected.Points || len(strings.Fields(criterion.Comment)) > 25 {
			return invalid()
		}
		verdict := "частично"
		if criterion.AwardedPoints == 0 {
			verdict = "не выполнено"
		} else if criterion.AwardedPoints == expected.Points {
			verdict = "выполнено"
		}
		if criterion.Verdict != verdict {
			return invalid()
		}
		sum += criterion.AwardedPoints
		if criterion.AwardedPoints < expected.Points {
			missed[criterion.Criterion] = true
		}
	}
	for _, point := range evaluation.MissedPoints {
		if !missed[point.Criterion] || strings.TrimSpace(point.Point) == "" {
			return invalid()
		}
		covered[point.Criterion] = true
	}
	for name := range missed {
		if !covered[name] {
			return invalid()
		}
	}
	critical := false
	for _, err := range evaluation.ErrorsFound {
		critical = critical || err.Critical
		if strings.TrimSpace(err.Description) == "" {
			return invalid()
		}
	}
	if sum != evaluation.Score || evaluation.CriticalError != critical || evaluation.Passed != (sum >= 7 && !critical) {
		return invalid()
	}
	if strings.TrimSpace(evaluation.Feedback) == "" || strings.TrimSpace(evaluation.MainLesson) == "" || len(strings.Fields(evaluation.MainLesson)) > 25 || strings.Contains(evaluation.Feedback, card.ReferenceAnswer) {
		return invalid()
	}
	missedRules := make([]string, 0, len(evaluation.MissedPoints))
	for _, point := range evaluation.MissedPoints {
		line := point.Point
		if point.Citation != nil {
			line += " (" + *point.Citation + ")"
		}
		missedRules = append(missedRules, line)
	}
	return domain.SandboxResponse{Evaluation: &evaluation, IsCorrect: evaluation.Passed, Score: evaluation.Score, Feedback: evaluation.Feedback, MissedRules: missedRules}, nil
}
