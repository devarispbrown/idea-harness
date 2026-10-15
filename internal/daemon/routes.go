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
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/gaps", s.handleGaps)
	m.HandleFunc("GET /api/v1/projects/{projectId}/gaps", s.handleProjectGaps)
	m.HandleFunc("POST /api/v1/projects/{projectId}/assertions/{id}/status", s.handleSetAssertionStatus)
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
	// Run events (runevents.go): a run's paged events, its SSE stream, steering
	// and its report; and the initiative's current run.
	m.HandleFunc("GET /api/v1/projects/{projectId}/runs/{runId}/events", s.handleListRunEvents)
	m.HandleFunc("GET /api/v1/projects/{projectId}/runs/{runId}/events/stream", s.handleRunEventStream)
	m.HandleFunc("POST /api/v1/projects/{projectId}/runs/{runId}/steer", s.handleSteerRun)
	m.HandleFunc("GET /api/v1/projects/{projectId}/runs/{runId}/report", s.handleRunReport)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/current-run", s.handleCurrentRun)

	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/execute", s.handleExecute)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/verify", s.handleVerify)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/quality", s.handleGetQuality)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/quality", s.handleMeasureQuality)
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
	m.HandleFunc("PUT /api/v1/projects/{projectId}/integrations/{name}", s.handleUpdateIntegration)

	// Spending caps (budget.go) and scheduled automations (automations.go).
	m.HandleFunc("GET /api/v1/projects/{projectId}/budget", s.handleProjectBudget)
	m.HandleFunc("PUT /api/v1/projects/{projectId}/budget", s.handleSetProjectBudget)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/budget", s.handleGetBudget)
	m.HandleFunc("PUT /api/v1/projects/{projectId}/initiatives/{id}/budget", s.handleSetBudget)
	m.HandleFunc("GET /api/v1/projects/{projectId}/automations", s.handleListAutomations)
	m.HandleFunc("PUT /api/v1/projects/{projectId}/automations/{name}", s.handleUpdateAutomation)
	m.HandleFunc("POST /api/v1/projects/{projectId}/automations/{name}/run", s.handleRunAutomation)

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
	m.HandleFunc("GET /api/v1/projects/{projectId}/executors/config", s.handleExecutorConfig)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/agent-output", s.handleAgentOutput)
	m.HandleFunc("DELETE /api/v1/projects/{projectId}/initiatives/{id}/partial-work", s.handleDiscardPartialWork)

	// Objectives & outcomes (outcomes.go).
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/objectives", s.handleListObjectives)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/objectives", s.handleCreateObjective)
	m.HandleFunc("PATCH /api/v1/projects/{projectId}/initiatives/{id}/objectives/{objectiveId}", s.handleUpdateObjective)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/objectives/{objectiveId}/requirements", s.handleLinkObjectiveRequirement)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/metrics", s.handleListMetrics)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/metrics", s.handleCreateMetric)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/metrics/measure", s.handleMeasureMetrics)
	m.HandleFunc("PATCH /api/v1/projects/{projectId}/initiatives/{id}/metrics/{metricId}", s.handleUpdateMetric)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/metrics/{metricId}/observations", s.handleListObservations)
	m.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/metrics/{metricId}/observations", s.handleRecordObservation)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/outcomes", s.handleOutcomes)

	// Review convergence and quality history for the web UI (graph_views.go).
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/review-telemetry", s.handleReviewTelemetry)
	m.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/quality/history", s.handleQualityHistory)

	// Remote browser sign-in (uisession.go): pairing codes, sessions, logout.
	m.HandleFunc("POST /api/v1/ui/pairing-codes", s.handleCreatePairingCode)
	m.HandleFunc("POST /api/v1/ui/login", s.handleLogin)
	m.HandleFunc("POST /api/v1/ui/logout", s.handleLogout)
	m.HandleFunc("GET /api/v1/ui/session", s.handleGetUISession)
	m.HandleFunc("GET /api/v1/ui/sessions", s.handleListUISessions)
	m.HandleFunc("DELETE /api/v1/ui/sessions", s.handleRevokeAllUISessions)
	m.HandleFunc("DELETE /api/v1/ui/sessions/{id}", s.handleRevokeUISession)

	// Web Push notifications (push.go): the VAPID key, and per-project
	// browser subscriptions -- each one a subscriber on the integration bus.
	m.HandleFunc("GET /api/v1/push/vapid-public-key", s.handlePushConfig)
	m.HandleFunc("GET /api/v1/projects/{projectId}/push/subscriptions", s.handleListPushSubscriptions)
	m.HandleFunc("POST /api/v1/projects/{projectId}/push/subscriptions", s.handleCreatePushSubscription)
	m.HandleFunc("PATCH /api/v1/projects/{projectId}/push/subscriptions/{id}", s.handleUpdatePushSubscription)
	m.HandleFunc("DELETE /api/v1/projects/{projectId}/push/subscriptions/{id}", s.handleDeletePushSubscription)
	m.HandleFunc("POST /api/v1/projects/{projectId}/push/subscriptions/{id}/test", s.handleTestPushSubscription)
	// Unauthenticated: the service worker renews with the subscription's own
	// renewal token (push.go, handleRenewPushSubscription).
	m.HandleFunc("POST /api/v1/projects/{projectId}/push/subscriptions/{id}/renew", s.handleRenewPushSubscription)

	// The API reference and the spec it renders. Deliberately outside /api/:
	// the auth middleware guards that prefix, and a token-gated spec would make
	// the docs page fetch a 401 into a blank screen.
	registerAPIDocs(m)
}
