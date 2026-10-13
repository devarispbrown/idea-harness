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
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/guidance", s.handleGuidance)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/changed", s.handleChanged)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/timeline", s.handleTimeline)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/costs", s.handleCosts)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/resume", s.handleResume)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/approve", s.handleApprove)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/revise", s.handleRevise)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/retry", s.handleRetry)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/advance", s.handleAdvance)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/docs", s.handleAddDoc)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/docs", s.handleListDocs)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/docs/{docId}", s.handleGetDoc)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/tasks", s.handleListTasks)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks", s.handleAddTask)
	m.HandleFunc("PATCH /api/v1/projects/{projectId}/initiatives/{id}/tasks/{taskId}", s.handleUpdateTask)
	m.HandleFunc("DELETE /api/v1/projects/{projectId}/initiatives/{id}/tasks/{taskId}", s.handleDeleteTask)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/{taskId}/cancel", s.handleCancelTask)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/reorder", s.handleReorderTasks)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/merge", s.handleMergeTasks)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/{taskId}/split", s.handleSplitTask)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/{taskId}/depends-on", s.handleSetTaskDependsOn)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/requirements/{reqId}/verification-task", s.handleGenerateVerificationTask)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/revise-plan", s.handleRevisePlan)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/reopen-phase", s.handleReopenPhase)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/approve-plan", s.handleApprovePlan)
	m.HandleFunc("POST /api/v1/projects/{projectId}/seen", s.handleMarkSeen)
	m.HandleFunc("GET /api/v1/projects/{projectId}/attention", s.handleAttention)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/why", s.handleWhy)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/impact", s.handleImpact)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/impact-counts", s.handleImpactCounts)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/stale/{staleId}/ack", s.handleAckStale)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/trace", s.handleTrace)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/diff", s.handleDiff)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/suggest-answers", s.handleSuggestAnswers)
	m.HandleFunc("GET /api/v1/logs", s.handleLogs)

	m.HandleFunc("GET /api/v1/projects/{projectId}/queue", s.handleQueue)

	m.HandleFunc("GET /api/v1/projects/{projectId}/providers", s.handleListProviders)
	m.HandleFunc("PUT /api/v1/projects/{projectId}/providers", s.handleUpdateProviders)
	m.HandleFunc("GET /api/v1/projects/{projectId}/models", s.handleListModels)

	m.HandleFunc("GET /api/v1/operations/{id}", s.handleGetOperation)
	m.HandleFunc("POST /api/v1/operations/{id}/cancel", s.handleCancelOperation)

	m.HandleFunc("POST /api/v1/projects/{projectId}/decisions/{id}/resolve", s.handleResolveDecision)
	m.HandleFunc("POST /api/v1/projects/{projectId}/decisions/{id}/reopen", s.handleReopenDecision)
	m.HandleFunc("GET /api/v1/projects/{projectId}/decisions/{id}/discussion", s.handleDecisionDiscussion)
	m.HandleFunc("POST /api/v1/projects/{projectId}/decisions/{id}/discuss", s.handleDecisionDiscuss)
	m.HandleFunc("POST /api/v1/projects/{projectId}/questions/{id}/answer", s.handleAnswerQuestion)

	m.HandleFunc("GET /api/v1/projects/{projectId}/artifacts/diff", s.handleArtifactDiff)
	m.HandleFunc("GET /api/v1/projects/{projectId}/artifacts/{id}/body", s.handleArtifactBody)
	m.HandleFunc("GET /api/v1/projects/{projectId}/artifacts/{id}/raw", s.handleArtifactRaw)
	m.HandleFunc("GET /api/v1/projects/{projectId}/artifacts/{id}/comments", s.handleListDocComments)
	m.HandleFunc("POST /api/v1/projects/{projectId}/artifacts/{id}/comments", s.handleCreateDocComment)
	m.HandleFunc("POST /api/v1/projects/{projectId}/comments/{id}/promote", s.handlePromoteDocComment)

	m.HandleFunc("POST /api/v1/projects/{projectId}/findings/{id}/disposition", s.handleFindingDisposition)
	m.HandleFunc("POST /api/v1/projects/{projectId}/findings/{id}/note", s.handleFindingNote)
	m.HandleFunc("GET /api/v1/projects/{projectId}/secret-allowlist", s.handleListSecretAllowlist)
	m.HandleFunc("DELETE /api/v1/projects/{projectId}/secret-allowlist/{id}", s.handleRevokeSecretAllowlist)

	m.HandleFunc("GET /api/v1/projects/{projectId}/activity", s.handleActivity)
	m.HandleFunc("GET /api/v1/projects/{projectId}/events", s.handleEvents)

	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/execute", s.handleExecute)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/verify", s.handleVerify)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/operations", s.handleListOperations)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/next", s.handleNext)
	m.HandleFunc("GET /api/v1/projects/{projectId}/change-sets/{id}", s.handleGetChangeSet)
	m.HandleFunc("GET /api/v1/projects/{projectId}/change-sets/{id}/checks", s.handleListChecks)
	m.HandleFunc("POST /api/v1/projects/{projectId}/change-sets/{id}/resolve-conflict", s.handleResolveConflict)
	m.HandleFunc("GET /api/v1/projects/{projectId}/change-sets/{id}/diff", s.handleChangeSetDiff)
	m.HandleFunc("POST /api/v1/projects/{projectId}/change-sets/{id}/review", s.handleReviewChangeSet)
	m.HandleFunc("POST /api/v1/projects/{projectId}/change-sets/{id}/apply", s.handleApplyChangeSet)
	m.HandleFunc("POST /api/v1/projects/{projectId}/change-sets/{id}/revert", s.handleRevertChangeSet)
	m.HandleFunc("GET /api/v1/projects/{projectId}/change-sets/{id}/pr-checks", s.handlePRChecks)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/pr", s.handleCreatePR)
	m.HandleFunc("GET /api/v1/projects/{projectId}/executors", s.handleListExecutors)
	m.HandleFunc("GET /api/v1/projects/{projectId}/integrations", s.handleIntegrationsStatus)
	m.HandleFunc("POST /api/v1/projects/{projectId}/integrations/{name}/test", s.handleIntegrationTest)

	// The initiative-scoped reads, on the nested path. These ten once answered
	// only at the flat `/projects/{projectId}/<resource>?initiative_id=` form,
	// which is why the same question had two shapes depending on the resource.
	// The flat forms are gone -- the CLI and the web UI moved over, and this
	// comment claimed both were still live for two audit rounds after they were
	// removed (docs/plans/api-consistency-and-ci-plan.md).
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/requirements", s.handleListRequirements)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/assertions", s.handleListAssertions)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/artifacts", s.handleListArtifacts)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/decisions", s.handleListDecisions)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/decisions/accept-recommendations", s.handleAcceptRecommendations)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/triage", s.handleTriage)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/questions", s.handleListQuestions)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/findings", s.handleListFindings)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/change-sets", s.handleListChangeSets)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/suggested-answers", s.handleListSuggestedAnswers)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/risk", s.handleRisk)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/doctor", s.handleDoctor)
	m.HandleFunc("PUT /api/v1/projects/{projectId}/executors", s.handleUpdateExecutors)
	m.HandleFunc("POST /api/v1/projects/{projectId}/executors/smoke", s.handleExecutorSmoke)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/agent-output", s.handleAgentOutput)
	m.HandleFunc("DELETE /api/v1/projects/{projectId}/initiatives/{id}/partial-work", s.handleDiscardPartialWork)

	// The API reference and the spec it renders. Deliberately outside /api/:
	// the auth middleware guards that prefix, and a token-gated spec would make
	// the docs page fetch a 401 into a blank screen.
	registerAPIDocs(m)
}
