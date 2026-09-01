package daemon


func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/v1/health", s.handleHealth)
	m.HandleFunc("GET /api/v1/version", s.handleVersion)

	m.HandleFunc("POST /api/v1/projects", s.handleCreateProject)
	m.HandleFunc("GET /api/v1/projects", s.handleListProjects)
	m.HandleFunc("GET /api/v1/projects/{projectId}", s.handleGetProject)

	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives", s.handleCreateInitiative)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives", s.handleListInitiatives)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}", s.handleGetInitiative)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/understanding", s.handleUnderstanding)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/next-move", s.handleNextMove)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/changed", s.handleChanged)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/resume", s.handleResume)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/approve", s.handleApprove)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/revise", s.handleRevise)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/advance", s.handleAdvance)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/tasks", s.handleListTasks)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/why", s.handleWhy)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/impact", s.handleImpact)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/trace", s.handleTrace)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/diff", s.handleDiff)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/suggest-answers", s.handleSuggestAnswers)
	m.HandleFunc("GET /api/v1/projects/{projectId}/suggested-answers", s.handleListSuggestedAnswers)
	m.HandleFunc("GET /api/v1/logs", s.handleLogs)

	m.HandleFunc("GET /api/v1/projects/{projectId}/providers", s.handleListProviders)
	m.HandleFunc("PUT /api/v1/projects/{projectId}/providers", s.handleUpdateProviders)
	m.HandleFunc("GET /api/v1/projects/{projectId}/models", s.handleListModels)

	m.HandleFunc("GET /api/v1/operations/{id}", s.handleGetOperation)
	m.HandleFunc("POST /api/v1/operations/{id}/cancel", s.handleCancelOperation)

	m.HandleFunc("GET /api/v1/projects/{projectId}/decisions", s.handleListDecisions)
	m.HandleFunc("POST /api/v1/projects/{projectId}/decisions/{id}/resolve", s.handleResolveDecision)
	m.HandleFunc("GET /api/v1/projects/{projectId}/decisions/{id}/discussion", s.handleDecisionDiscussion)
	m.HandleFunc("POST /api/v1/projects/{projectId}/decisions/{id}/discuss", s.handleDecisionDiscuss)
	m.HandleFunc("GET /api/v1/projects/{projectId}/questions", s.handleListQuestions)
	m.HandleFunc("POST /api/v1/projects/{projectId}/questions/{id}/answer", s.handleAnswerQuestion)

	m.HandleFunc("GET /api/v1/projects/{projectId}/requirements", s.handleListRequirements)
	m.HandleFunc("GET /api/v1/projects/{projectId}/assertions", s.handleListAssertions)
	m.HandleFunc("GET /api/v1/projects/{projectId}/artifacts", s.handleListArtifacts)
	m.HandleFunc("GET /api/v1/projects/{projectId}/artifacts/{id}/body", s.handleArtifactBody)
	m.HandleFunc("GET /api/v1/projects/{projectId}/artifacts/{id}/raw", s.handleArtifactRaw)

	m.HandleFunc("GET /api/v1/projects/{projectId}/findings", s.handleListFindings)
	m.HandleFunc("POST /api/v1/projects/{projectId}/findings/{id}/disposition", s.handleFindingDisposition)
	m.HandleFunc("POST /api/v1/projects/{projectId}/findings/{id}/note", s.handleFindingNote)
	m.HandleFunc("GET /api/v1/projects/{projectId}/doctor", s.handleDoctor)

	m.HandleFunc("GET /api/v1/projects/{projectId}/activity", s.handleActivity)
	m.HandleFunc("GET /api/v1/projects/{projectId}/events", s.handleEvents)

	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/execute", s.handleExecute)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/verify", s.handleVerify)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/next", s.handleNext)
	m.HandleFunc("GET /api/v1/projects/{projectId}/change-sets", s.handleListChangeSets)
	m.HandleFunc("GET /api/v1/projects/{projectId}/change-sets/{id}", s.handleGetChangeSet)
	m.HandleFunc("GET /api/v1/projects/{projectId}/change-sets/{id}/diff", s.handleChangeSetDiff)
	m.HandleFunc("POST /api/v1/projects/{projectId}/change-sets/{id}/review", s.handleReviewChangeSet)
	m.HandleFunc("POST /api/v1/projects/{projectId}/change-sets/{id}/apply", s.handleApplyChangeSet)
	m.HandleFunc("POST /api/v1/projects/{projectId}/change-sets/{id}/revert", s.handleRevertChangeSet)
	m.HandleFunc("GET /api/v1/projects/{projectId}/risk", s.handleRisk)
	m.HandleFunc("GET /api/v1/projects/{projectId}/executors", s.handleListExecutors)
	m.HandleFunc("PUT /api/v1/projects/{projectId}/executors", s.handleUpdateExecutors)
}
